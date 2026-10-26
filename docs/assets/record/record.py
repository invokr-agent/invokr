"""Record the README demo GIF (and a still PNG) for the Invokr TUI.

Ported from tuikit's docs recorder: no vhs, tmux, asciinema or ImageMagick —
the agent runs in a pty, pyte plays its escape sequences into a screen buffer,
and Pillow draws each frame. The GIF is therefore a render of what the program
actually emitted rather than a capture of one machine's terminal theme.

    uv venv /tmp/rec && uv pip install --python /tmp/rec/bin/python pyte pillow
    go build -o /tmp/invokr .
    /tmp/rec/bin/python docs/assets/record/record.py \
        docs/assets/demo.gif docs/assets/demo.png -- /tmp/invokr agent

The keystroke timeline lives in SCRIPTS below; RECORD=<name> picks one. The
demo drives a real turn against the configured model, so the delays are the
loose part: they have to cover a model's think time, not a fixed animation.
"""

import fcntl
import os
import re
import pty
import select
import struct
import sys
import termios
import time

import pyte
from wcwidth import wcwidth
from PIL import Image, ImageDraw, ImageFont

PROMPT = os.environ.get(
    "PROMPT",
    "run go build ./tools/core — does it compile?",
)
DISPLAY_MODEL = os.environ.get("DEMO_MODEL_LABEL", "")
ACTUAL_MODEL = os.environ.get("INVOKR_MODEL", "")

COLS, ROWS = 100, 32
FONT_PATH = "/usr/share/fonts/TTF/JetBrainsMonoNerdFontMono-Regular.ttf"
# JetBrains Mono has no U+25D0 ◐, the running-spinner glyph, and draws it as a
# .notdef box. Anything monospace that does have it works as a stand-in for the
# handful of characters in that position.
FALLBACK_FONT_PATH = "/usr/share/fonts/TTF/FiraCodeNerdFontMono-Regular.ttf"
# Emoji in the status bar (📁 and friends) are in neither mono font. Noto Color
# Emoji is a bitmap font that only loads at 109px, so its glyphs are drawn at
# that size and scaled down into the cell.
EMOJI_FONT_PATH = "/usr/share/fonts/noto/NotoColorEmoji.ttf"
EMOJI_SIZE = 109
FONT_SIZE = 13
PAD = 16
FRAME_MS = 100  # sample rate; a shared palette keeps even this cheap
# Gap in pty output that marks a repaint as finished. The TUI writes a frame in
# several bursts, so a short gap samples between them and captures a torn screen
# — a stale tail left on a line, a half-written glyph. 30ms is past the bursts
# and still well inside the spinner's 125ms tick.
QUIET_MS = 30
BG = (18, 19, 26)
FG = (200, 205, 212)
# Shared-palette size. The gradient wordmark is the only thing here with a wide
# range; 128 covers it without the banding a smaller table shows on its ramp.
PALETTE_COLORS = 128

class Typed(str):
    """Input to send a character at a time rather than in one paste.

    A whole prompt written to the pty at once appears in the input box between
    two frames, which reads as a jump cut. Typing it is the only part of the
    demo whose pace the recorder controls rather than the model.
    """


# Milliseconds between keystrokes of a Typed step: faster than a person, slow
# enough to read as typing. Below FRAME_MS, so a frame lands every couple of
# characters rather than every one — which is what the input box looks like
# under a fast typist anyway, at half the frames.
TYPE_MS = 55


def expand(script):
    """Split Typed steps into one step per character."""
    out = []
    for delay, keys, capture in script:
        if isinstance(keys, Typed):
            out.extend((TYPE_MS / 1000, char, capture) for char in keys)
            out.append((delay, None, capture))
        else:
            out.append((delay, keys, capture))
    return out


SCRIPTS = {
    # The README demo: a prompt, the tiered shell approval it raises, the
    # answer after it is approved, and /usage on the turn it just spent.
    # Delays are generous because a local model's latency, not the UI, sets
    # the pace.
    "demo": ([
        (2.0, None, True),                 # the banner, before anything is typed
        (0.6, Typed(PROMPT), True),        # the prompt, typed a character at a time
        (1.0, "\r", True),                 # send it
        (40.0, None, True),                # thinking, then the approval panel
        (0.4, "a", True),                  # always-allow go build, so follow-ups run
        (110.0, None, True),               # the tool runs and the answer streams
        # Written in one go rather than Typed: while the completion popup is
        # open, pyte keeps the transcript text under it instead of repainting
        # those rows (a real terminal does not), so the demo skips past the
        # popup rather than pausing on a frame that misrenders here.
        (1.4, "/usage\r", True),         # run it
        (2.5, None, True),                 # the per-category token breakdown, open
        (0.9, "j", True),                  # walk the rows: the bar follows the cursor
        (2.0, "j", True),                  # onto instructions: the hint offers enter
        (3.0, "\r", True),                 # expand it into its prompt sections
        (1.2, "\r", True),                 # and fold it back
        (2.5, "j", True),                  # into history: the other group lights up
    ], 20.0),                              # still: the approval panel, open
    # One tiny turn, then /usage: a quick check of how the panel renders.
    "usage": ([
        (2.0, None, False),
        (0.5, "say hi in one word", False),
        (0.5, "\r", False),
        (120.0, None, False),
        (2.0, "/usage\r", True),
        (0.9, "j", True),
        (2.0, "j", True),
    ], 3.0),
}
RECORD = os.environ.get("RECORD", "demo")
SCRIPT, STILL_AT = SCRIPTS[RECORD]
SCRIPT = expand(SCRIPT)

# A clocked recording stamps every frame with elapsed time and keeps sampling
# while the screen is unchanged. Both are needed to record a *freeze*: a program
# that has stopped drawing emits nothing, so the usual dirty-and-deduplicate
# path would collapse the freeze into a single frame and the clock would appear
# to stop with it.
# No clocked recordings here: nothing being demoed is about a freeze.
CLOCK = False

# pyte reports ANSI names; the rest arrive as bare hex.
NAMED = {
    "black": (40, 44, 52), "red": (224, 108, 117), "green": (152, 195, 121),
    "brown": (229, 192, 123), "yellow": (229, 192, 123), "blue": (97, 175, 239),
    "magenta": (198, 120, 221), "cyan": (86, 182, 194), "white": (200, 205, 212),
    "brightblack": (120, 128, 140), "brightred": (255, 130, 139),
    "brightgreen": (166, 226, 120), "brightbrown": (255, 214, 130),
    "brightyellow": (255, 214, 130), "brightblue": (120, 190, 255),
    "brightmagenta": (215, 140, 235), "brightcyan": (110, 205, 215),
    "brightwhite": (255, 255, 255),
}


def color(name, default):
    if not name or name == "default":
        return default
    if name in NAMED:
        return NAMED[name]
    if len(name) == 6:
        try:
            return tuple(int(name[i:i + 2], 16) for i in (0, 2, 4))
        except ValueError:
            pass
    return default


class Screen(pyte.Screen):
    """pyte.Screen that widens text-presentation emoji followed by VS16.

    A terminal draws U+1F5DC U+FE0F two columns wide; pyte counts one, so every
    later cell on the row lands a column early and the text overwrites itself.
    """

    last_char = " "

    def draw(self, data):
        data = re.sub(".\ufe0f", lambda m: m[0][0] + (" " if wcwidth(m[0][0]) == 1 else ""), data)
        if data:
            self.last_char = data[-1]
        super().draw(data)

    def repeat_last(self, count=None):
        """CSI b (REP), which pyte lacks and bubbletea v2 uses for runs like a bar or a border."""
        self.draw(self.last_char * (count or 1))

    def scroll_down(self, count=None):
        """CSI T, which pyte lacks: a reverse index at the top margin scrolls the region down."""
        y = self.cursor.y
        self.cursor.y = self.margins.top if self.margins else 0
        for _ in range(count or 1):
            self.reverse_index()
        self.cursor.y = y


class Stream(pyte.ByteStream):
    csi = {**pyte.ByteStream.csi, "T": "scroll_down", "b": "repeat_last"}


def run(command):
    """Drive command in a pty, returning screen snapshots as (t, buffer)."""
    screen = Screen(COLS, ROWS)
    stream = Stream(screen)
    pid, fd = pty.fork()
    if pid == 0:
        os.environ.update(TERM="xterm-256color", COLORTERM="truecolor", LINES=str(ROWS), COLUMNS=str(COLS))
        os.execvp(command[0], command)
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", ROWS, COLS, 0, 0))

    frames, start, capturing = [], time.time(), False
    last, dirty = 0.0, False
    for delay, keys, capture in SCRIPT:
        if keys:
            os.write(fd, keys.encode())
        capturing = capturing or capture
        deadline = time.time() + delay
        while time.time() < deadline:
            ready, _, _ = select.select([fd], [], [], QUIET_MS / 1000)
            if ready:
                try:
                    data = os.read(fd, 65536)
                except OSError:
                    data = b""
                if not data:
                    break
                stream.feed(data)
                dirty = True
                continue
            # The pty has gone quiet, so the last repaint is complete. Sampling
            # while a write is still in flight catches the screen mid-escape-
            # sequence and tears the frame — a program that repaints on every
            # character is almost always mid-write. Waiting for the gap is what
            # makes each captured frame a whole one.
            now = time.time()
            if capturing and (dirty or CLOCK) and now - last >= FRAME_MS / 1000:
                last, dirty = now, False
                frames.append((now - start, snapshot(screen)))
    os.write(fd, b"\x03")
    time.sleep(0.3)
    os.close(fd)
    os.waitpid(pid, os.WNOHANG)
    return frames


def snapshot(screen):
    rows = [[screen.buffer[y][x] for x in range(COLS)] for y in range(ROWS)]
    if DISPLAY_MODEL and ACTUAL_MODEL:
        for row in rows:
            # A wide glyph's second cell has empty data; count it so the index is a column.
            start = "".join(char.data or " " for char in row).find(ACTUAL_MODEL)
            if start >= 0:
                # Swap the name in and shift the rest of the bar left, padding the right edge.
                label = DISPLAY_MODEL[:len(ACTUAL_MODEL)]
                row[start:] = ([row[start + i]._replace(data=c) for i, c in enumerate(label)]
                               + row[start + len(ACTUAL_MODEL):]
                               + [row[-1]._replace(data=" ")] * (len(ACTUAL_MODEL) - len(label)))
    return rows


def glyph_font(char, font, fallback):
    """font, unless it draws char as .notdef and fallback has the glyph.

    A missing glyph is not an error in Pillow — it renders the font's box and
    carries on — so the only way to spot one is to compare its bitmap against a
    codepoint that is certainly absent from every font.
    """
    if fallback is None:
        return font
    if char not in _GLYPH_FONT:
        missing = bytes(font.getmask(char)) == bytes(font.getmask(NOTDEF))
        _GLYPH_FONT[char] = fallback if missing else font
    return _GLYPH_FONT[char]


# An unassigned plane-15 codepoint: no font has it, so its bitmap is .notdef.
NOTDEF = "\U000FFFFD"
_GLYPH_FONT = {}
FALLBACK = None
EMOJI = None
_MISSING = {}


def missing(char, font):
    """Whether font draws char as .notdef."""
    key = (char, id(font))
    if key not in _MISSING:
        _MISSING[key] = bytes(font.getmask(char)) == bytes(font.getmask(NOTDEF))
    return _MISSING[key]


_EMOJI_CACHE = {}


def paste_emoji(image, char, px, py, cell_w, cell_h):
    """Draw a colour emoji two cells wide, scaled from Noto's fixed bitmap size."""
    if char not in _EMOJI_CACHE:
        glyph = Image.new("RGBA", (EMOJI_SIZE * 2, EMOJI_SIZE * 2), (0, 0, 0, 0))
        ImageDraw.Draw(glyph).text((0, 0), char, font=EMOJI, embedded_color=True)
        box = glyph.getbbox()
        if box:
            glyph = glyph.crop(box)
        side = min(cell_h, 2 * cell_w) - 2
        glyph.thumbnail((side, side), Image.LANCZOS)
        _EMOJI_CACHE[char] = glyph
    glyph = _EMOJI_CACHE[char]
    image.paste(glyph, (px + 1, py + (cell_h - glyph.height) // 2), glyph)


def draw(buffer, font, cell_w, cell_h, t=None):
    image = Image.new("RGB", (COLS * cell_w + 2 * PAD, ROWS * cell_h + 2 * PAD), BG)
    canvas = ImageDraw.Draw(image)
    for y, row in enumerate(buffer):
        for x, char in enumerate(row):
            px, py = PAD + x * cell_w, PAD + y * cell_h
            bg = color(char.bg, BG)
            if char.reverse:
                bg = color(char.fg, FG)
            if bg != BG:
                canvas.rectangle([px, py, px + cell_w, py + cell_h], fill=bg)
            if char.data.strip():
                fg = BG if char.reverse else color(char.fg, FG)
                if EMOJI and missing(char.data, font) and (FALLBACK is None or missing(char.data, FALLBACK)):
                    paste_emoji(image, char.data, px, py, cell_w, cell_h)
                else:
                    canvas.text((px, py), char.data, font=glyph_font(char.data, font, FALLBACK), fill=fg)
    if t is not None:
        # A wall clock, drawn by the recorder rather than the program, so two
        # recordings of different builds share one timebase.
        label = f"{t:5.1f}s"
        w = font.getlength(label)
        canvas.rectangle([image.width - PAD - w - 8, 2, image.width, PAD + cell_h],
                         fill=BG)
        canvas.text((image.width - PAD - w, 4), label, font=font, fill=(120, 190, 255))
    return image


def quantize(images):
    """Map every frame onto one shared palette, without dithering.

    Quantizing each frame on its own picks a slightly different palette for
    each, so flat terminal colors shimmer between frames and any GIF optimizer
    then has to store whole frames it would otherwise skip. The palette is
    built from a strip of samples across the run rather than from one frame, so
    colors that only appear late (a filled meter, a warning) are in it too.
    Dithering is off for the same reason: nothing here is a photograph, and
    dither noise is exactly what reads as a glitch on a wall of flat text.
    """
    sample = images[0].copy()
    strip = images[:: max(1, len(images) // 24)]
    montage = Image.new("RGB", (sample.width, sample.height * len(strip)))
    for i, image in enumerate(strip):
        montage.paste(image, (0, i * sample.height))
    palette = montage.quantize(colors=PALETTE_COLORS, method=Image.Quantize.MAXCOVERAGE)
    return [image.quantize(palette=palette, dither=Image.Dither.NONE) for image in images]


def main():
    gif_path, png_path = sys.argv[1], sys.argv[2]
    command = sys.argv[sys.argv.index("--") + 1:]

    frames = run(command)
    global FALLBACK
    font = ImageFont.truetype(FONT_PATH, FONT_SIZE)
    if os.path.exists(FALLBACK_FONT_PATH):
        FALLBACK = ImageFont.truetype(FALLBACK_FONT_PATH, FONT_SIZE)
    global EMOJI
    if os.path.exists(EMOJI_FONT_PATH):
        EMOJI = ImageFont.truetype(EMOJI_FONT_PATH, EMOJI_SIZE)
    cell_w = round(font.getlength("M"))
    ascent, descent = font.getmetrics()
    cell_h = ascent + descent

    images, times, previous = [], [], None
    for t, buffer in frames:
        if buffer == previous and not CLOCK:
            continue
        previous = buffer
        images.append(draw(buffer, font, cell_w, cell_h, t - frames[0][0] if CLOCK else None))
        times.append(t)
    if not images:
        sys.exit("no frames captured")

    # Skip long unchanged waits without accelerating typed characters.
    durations = [min(2000, int(1000 * (b - a))) for a, b in zip(times, times[1:])] + [10000]  # hold the /usage overlay long enough to read
    paletted = quantize(images)
    paletted[0].save(gif_path, save_all=True, append_images=paletted[1:],
                     duration=durations, loop=0, optimize=True, disposal=1)

    still = min(range(len(times)), key=lambda i: abs(times[i] - STILL_AT))
    images[still].save(png_path)
    print(f"{gif_path}: {len(images)} frames, {sum(durations)/1000:.1f}s")
    print(f"{png_path}: frame at t={times[still]:.1f}s")


if __name__ == "__main__":
    main()
