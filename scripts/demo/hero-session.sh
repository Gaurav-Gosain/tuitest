#!/usr/bin/env bash
# Builds the two-pane screen the hero recording films, and is only ever run by
# hero.tape. The top pane is the program under test as tuitest drives it; the
# bottom pane is tuitest's own command trace, tailed as it is written. Nothing
# on either side is staged: the trace is the replayer's -echo output, and the
# screen above it is the program's own bytes mirrored out of the pseudo-terminal.
#
# The recording runs against a throwaway clone of this repository under a
# throwaway HOME, so the program shows ~/tuitest wherever the repository
# actually lives and picks up the palette below rather than whatever the
# operator has configured.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)

sandbox=$(mktemp -d)
sock="$sandbox/tmux.sock"
trap 'tmux -S "$sock" kill-server 2>/dev/null; rm -rf "$sandbox"' EXIT

git clone --quiet --local --no-hardlinks "$root" "$sandbox/tuitest"
mkdir -p "$sandbox/.config/lazygit"

# The accent green and the greys are the banner's, so the program reads as part
# of this project's images rather than as a screenshot of someone's dotfiles.
cat >"$sandbox/.config/lazygit/config.yml" <<'CFG'
gui:
  nerdFontsVersion: "3"
  # A wider side panel keeps commit subjects readable at this width, and it
  # narrows the diff pane, which is the region that repaints on every keypress
  # and so is what the recording's file size is mostly made of.
  sidePanelWidth: 0.42
  showBottomLine: true
  showCommandLog: false
  theme:
    activeBorderColor: ["#7fd88f", bold]
    inactiveBorderColor: ["#1f2a38"]
    optionsTextColor: ["#7fb4d8"]
    selectedLineBgColor: ["#1f2a38"]
    inactiveViewSelectedLineBgColor: ["#1f2a38"]
    cherryPickedCommitFgColor: ["#0b0e14"]
    cherryPickedCommitBgColor: ["#7fd88f"]
    unstagedChangesColor: ["#e88388"]
git:
  # The recording must not depend on a network, and the fetch spinner would
  # animate through every frame of a GIF that is otherwise still between inputs.
  autoFetch: false
  autoRefresh: false
disableStartupPopups: true
notARepository: quit
CFG

cat >"$sandbox/tmux.conf" <<'TMUX'
set -g default-terminal "tmux-256color"
set -ga terminal-overrides ",*:Tc"
set -g status off
set -g pane-border-status top
set -g pane-border-format " #[fg=#7fd88f]#{pane_title} "
set -g pane-border-style "fg=#1f2a38"
set -g pane-active-border-style "fg=#1f2a38"
set -g pane-border-lines single
set -g escape-time 0
TMUX

trace="$sandbox/trace"
: >"$trace"

# The program pane runs this rather than a one-liner wedged into the tmux
# command list: the trailing hold is what keeps the last frame on screen, and
# the clear wipes the terminal queries lazygit left on the normal screen before
# it switched to the alternate one.
cat >"$sandbox/drive.sh" <<DRIVE
#!/usr/bin/env bash
sleep 0.8
tuitest replay "$root/scripts/demo/tapes/hero.tape" 2>"$trace"
status=\$?
sleep 1.2
clear
if [ "\$status" -eq 0 ]; then
	printf '\n  tuitest replay finished: every assertion passed, exit %d\n' "\$status"
else
	printf '\n  tuitest replay failed: exit %d\n' "\$status"
fi
sleep 5
tmux -S "$sock" kill-server
DRIVE
chmod +x "$sandbox/drive.sh"

export HOME="$sandbox"
cd "$sandbox/tuitest"

# The trace pane starts first so it is already tailing when the replay opens,
# and the program pane is split in above it so the program lands on top. The
# leading pause covers tmux drawing its borders; without it the first frames
# film a half-built screen.
tmux -S "$sock" -f "$sandbox/tmux.conf" new-session -s hero \
	"tail -n +1 -f '$trace' | grep --line-buffered '^> ' | sed -u 's/^> //'" \; \
	select-pane -T "the input tuitest is sending" \; \
	split-window -vb -l 23 "$sandbox/drive.sh" \; \
	select-pane -T "lazygit, driven through a pseudo-terminal"
