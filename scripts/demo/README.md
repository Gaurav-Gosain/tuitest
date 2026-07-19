# demo

The recordings in the README. Each `.tape` here is a [vhs][] script; each one
drives the real `tuitest` binary against a real program, so a recording that
looks wrong means the tool did something wrong.

```sh
scripts/demo/record.sh            # every recording in the README
scripts/demo/record.sh run        # just one
```

`record.sh` builds `tuitest` and the two fixtures into a temporary directory and
puts it first on `PATH`, so the recordings show short commands rather than build
paths, and nothing is installed on the machine running them.

| tape | what it shows |
| --- | --- |
| `hero.tape` | the README's first image: `lazygit` being driven hard, with the command trace under it |
| `run.tape` | a tape passing against `less`, then a stale assertion failing with a diff and the screen |
| `snap.tape` | `snap` printing what `vim` draws, at two widths |
| `fuzz.tape` | `fuzz` finding the fixture's panic and minimising it to a tape that replays it |
| `record-replay.tape` | `record` producing a tape by hand, then replaying it (not in the README, see below) |

`tapes/` holds the tuitest tapes the recordings run, rather than heredocs inside
the vhs scripts, so they can be run on their own:

```sh
tuitest run scripts/demo/tapes/readme.tape        # exits 0
tuitest run scripts/demo/tapes/readme-stale.tape  # exits 1, on purpose
```

`readme-stale.tape` asserts wording this README no longer uses. It is supposed
to fail: it is the fixture for the failure output, and it will keep failing for
the same reason as long as the summary line reads the way it does.

## Why record-replay is not in the README

The round trip is correct and the recorded tape replays green. But `record`
emits one `Type` command and one inferred `Wait` per character, so typing
`hello` becomes ten lines of tape instead of `Type "hello"` and a single wait.
Widening `--quiet` does not merge them. The recording is faithful; it just reads
far worse than the tape language deserves, and a newcomer's first look at a tape
should not be a wall of single letters. Put it back in the README once
consecutive printable input is coalesced.

## House style

The palette is the banner's, so the images read as one set: `#0b0e14`
background, `#e6e9ef` text, the same green the wordmark's tail carries. It lives
in `common.tape`, which every recording sources. Type is JetBrains Mono Nerd
Font Mono at 15px, large enough to stay legible after GitHub scales the image
down.

Width is set per recording, from the longest line that must not wrap: the
failure diff in `run.tape` needs 95 columns, `fuzz.tape` needs the whole command
on one line. Height is set to the tallest moment, because trailing blank rows
are the easiest way to make a recording look careless.

`hero.tape` is the one recording that films a screen rather than a shell.
`hero-session.sh` builds that screen: a tmux session whose top pane is the
program as `tuitest replay` renders it, and whose bottom pane tails the
replayer's `-echo` trace as it is written. Neither pane is staged. It runs
against a throwaway clone of this repository under a throwaway `HOME`, so the
program shows `~/tuitest` wherever the repository actually lives and takes its
palette from the config the script writes rather than from the operator's.

It is also the one recording with a post-pass. A keypress there repaints a whole
diff pane, so the raw capture lands near a megabyte; `record.sh` requantises it
to twenty-four colours, which is where the size stops falling and is still
pixel-identical to the source, because the palette is the sixteen terminal
colours and a few blends. The other recordings are left alone: they are mostly
still text, and a second pass only bands the glyphs.

[vhs]: https://github.com/charmbracelet/vhs
