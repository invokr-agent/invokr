"""Screenshot the UX gallery: render each .ans file TestUXGallery wrote to a PNG.

    UX_GALLERY=$PWD/docs/assets/record/gallery go test ./cmd/agentui -run TestUXGallery -count=1
    /tmp/rec/bin/python docs/assets/record/snap.py

The gallery folder is gitignored; pass another folder as the first argument.

Uses record.py's pyte screen and Pillow renderer, so a still matches a GIF frame.
"""

import glob
import os
import sys

from PIL import ImageFont

import record

GALLERY = os.path.join(os.path.dirname(os.path.abspath(__file__)), "gallery")


def snap(path, font, cell_w, cell_h):
    text = open(path, encoding="utf-8").read().rstrip("\n")
    lines = text.split("\n")
    record.ROWS, record.COLS = len(lines), max(len(record.re.sub(r"\x1b\[[0-9;]*m", "", l)) for l in lines)
    # Light renders sit on a light page, as they would in a light terminal.
    record.BG, record.FG = ((250, 250, 250), (40, 40, 40)) if "-light-" in path else ((18, 19, 26), (200, 205, 212))
    screen = record.Screen(record.COLS, record.ROWS)
    record.Stream(screen).feed(text.replace("\n", "\r\n").encode())
    record.draw(record.snapshot(screen), font, cell_w, cell_h).save(path[:-4] + ".png")
    os.remove(path)  # the raw terminal output is only the renderer's input


def main():
    font = ImageFont.truetype(record.FONT_PATH, record.FONT_SIZE)
    if os.path.exists(record.FALLBACK_FONT_PATH):
        record.FALLBACK = ImageFont.truetype(record.FALLBACK_FONT_PATH, record.FONT_SIZE)
    if os.path.exists(record.EMOJI_FONT_PATH):
        record.EMOJI = ImageFont.truetype(record.EMOJI_FONT_PATH, record.EMOJI_SIZE)
    ascent, descent = font.getmetrics()
    for path in sorted(glob.glob(os.path.join(sys.argv[1] if len(sys.argv) > 1 else GALLERY, "*.ans"))):
        snap(path, font, round(font.getlength("M")), ascent + descent)
        print(path[:-4] + ".png")


if __name__ == "__main__":
    main()
