# CLI output

How non-TUI commands render. The rule is one sentence: **structure is the
same everywhere, color is added only when a human is watching, and anything a
script should parse has `--json`.**

## One palette

`cmd/render.go` draws from `surface/ui`'s theme — the same palette the Bubble
Tea TUI renders with. Command output defines no color of its own, so a failed
tool is the same red in `invokr exec` as in the TUI, and a palette change lands
in both at once.

The palette has a light and a dark variant. The CLI picks between them by
asking the terminal for its background color, once per process; without a
terminal on both stdin and stdout there is nobody to ask, and it falls back to
dark, which is what the TUI defaults to as well.

## Painting is decided once, per writer

`newRenderer(w)` resolves a `tuikit.Painter` for the destination: real painting
when `w` is a terminal and `NO_COLOR` is unset, and `tuikit.Plain` — which
returns text untouched — otherwise. Call sites never test for a terminal
themselves; that is how half a command's output ends up colored.

Consequences worth stating:

- `invokr sessions | grep`, `invokr doctor > report.txt` and a CI log all get
  byte-identical text with no escape sequences. There is a test asserting it.
- `NO_COLOR` means "do not paint", not "change format". A user who sets it
  still gets the table, not JSON. See <https://no-color.org>.
- Progress output goes to stderr and answers go to stdout, so redirecting
  stdout keeps the answer clean whether or not color was on.

## Tables

Listings use `tuikit`'s `Theme.Table`, which measures display width rather than
bytes — so a painted cell still lines up with an unpainted one, and a CJK or
emoji title does not shear the column. Long cells are elided in the middle
(`…`), keeping both ends, because that is where the distinguishing part of an
ID or a path lives.

An empty listing prints a sentence saying what to do instead, not an empty
table: a bare header is indistinguishable from a command that failed quietly.

## What color is allowed to mean

Color is redundant, never load-bearing — the active profile is marked `*` as
well as painted, and every status word is spelled out. Grading of status words
(`ok` green, `failed` red, anything unfamiliar amber) lives in
`tuikit.ClassifyStatus`, shared with the TUI.

One local override: a job's approval policy of `allow` is painted as a danger
rather than as a success. The generic vocabulary reads "allow" as a good
outcome; on a scheduled job it means every tool call is approved with nobody
watching, which is the one field in that table worth catching an eye.
