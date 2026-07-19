#!/usr/bin/env bash
# Regenerates every demo asset in docs/images from the vhs tapes beside this
# script. Run it from anywhere; it works in the repository root.
#
#   scripts/demo/record.sh            # every recording
#   scripts/demo/record.sh run fuzz   # only the named ones
#
# Requires vhs (github.com/charmbracelet/vhs), ImageMagick, Go, and the fonts
# the tapes ask for (JetBrains Mono). The recordings drive the real binaries
# built here, against real programs: less paging this repository's README, vim
# opening a source file, and the deliberately buggy fixture in testdata.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$root"

bin=$(mktemp -d)
trap 'rm -rf "$bin"' EXIT

echo "building into $bin"
go build -o "$bin/tuitest" ./cmd/tuitest
go build -o "$bin/buggytui" ./testdata/buggytui
go build -o "$bin/echotui" ./testdata/echotui
export PATH="$bin:$PATH"

mkdir -p docs/images

tapes=("$@")
if [ ${#tapes[@]} -eq 0 ]; then
	# record-replay.tape is deliberately not in this list and its output is not
	# in the README. The round trip is correct, but `record` emits one Type plus
	# one inferred Wait per character, so typing "hello" becomes ten lines of
	# tape rather than `Type "hello"`. Widening --quiet does not merge them. The
	# recording is faithful and replays green; it just reads far worse than the
	# tape language deserves. Ask for it by name to regenerate it, and put it in
	# the README once consecutive printable input is coalesced.
	tapes=(snap run fuzz)
fi

for name in "${tapes[@]}"; do
	echo "recording $name"
	# The fuzz recording writes reproductions; start from an empty corpus so a
	# rerun explores rather than replaying the last run's findings.
	rm -rf .demo-corpus
	vhs "scripts/demo/$name.tape"
	rm -rf .demo-corpus
done

# vhs already emits frame-differenced GIFs with a tight palette, so there is no
# post-pass here; anything more aggressive visibly banded the text. Just report
# the sizes, since the README budget is what actually matters.
for name in "${tapes[@]}"; do
	gif="docs/images/$name.gif"
	[ -f "$gif" ] || continue
	printf '%-28s %s\n' "$gif" "$(du -h "$gif" | cut -f1)"
done
