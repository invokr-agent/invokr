# Recording the README demo GIF

```bash
make record
```

That is the whole thing: it creates the venv if missing, builds the agent,
records a real turn, and writes `docs/assets/demo.gif` plus a still PNG. It
needs `uv`, a model server the agent can reach, and takes about a minute.
ImageMagick is optional — it only shrinks the result.

Knobs, all environment variables:

| variable | default | what it does |
| --- | --- | --- |
| `PROMPT` | the `go build ./tools/core` demo | the prompt that gets typed |
| `OUT` | `docs/assets/demo.gif` | where the GIF goes (preview elsewhere first) |
| `STILL` | `OUT` with a `.png` suffix | the still frame |
| `RECORD` | `demo` | which timeline in `record.py`'s `SCRIPTS` to play |
| `INVOKR_FLAGS` | — | flags for the binary, e.g. `--base-url=…` |
| `COLORS` | 96 | palette size for the ImageMagick pass |
| `DEMO_MODEL_LABEL` | — | displayed model name in rendered frames; `INVOKR_MODEL` still selects the model used |

## How it works

No `vhs`, `tmux`, `asciinema` or Pango: `record.py` runs the agent in a pty,
[pyte](https://pypi.org/project/pyte/) plays its escape sequences into a screen
buffer, and Pillow draws each frame. The GIF is a render of what the program
emitted rather than a capture of one machine's terminal theme, so it reproduces
anywhere Python does. It is a port of tuikit's `scripts/record.py`.

Three details are load-bearing, and each was a visible glitch before it was:

- **`QUIET_MS`** — a frame is only sampled once the pty has gone quiet. The TUI
  writes a screen in several bursts, so sampling too eagerly captures a torn
  screen: a stale tail left on a line, a half-drawn glyph.
- **One shared palette** — every frame is quantized against a palette built
  from samples across the whole run, without dithering. Per-frame palettes make
  flat colors shimmer and stop the GIF optimizer from skipping frames; it was
  the difference between 1.7 MB and 170 KB.
- **`FALLBACK_FONT_PATH`** — JetBrains Mono has no `◐`, the spinner glyph, and
  draws it as a box. Characters the primary font is missing are drawn from the
  fallback.

Because the demo drives a live model, the delays in `SCRIPTS` are the fragile
part: they cover think time, not a fixed animation. If the approval panel or the
answer lands outside its window, re-tune them.
