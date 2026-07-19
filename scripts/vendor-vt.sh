#!/usr/bin/env sh
# vendor-vt.sh re-syncs internal/vt from a tuios checkout and records the exact
# upstream commit in internal/vt/UPSTREAM. See internal/vt/VENDOR.md.
#
# Usage:
#   scripts/vendor-vt.sh [-n] /path/to/tuios [commit]
#
#   -n   report drift and change nothing.
#
# Only non-test .go files are copied. doc.go and emulator_test.go belong to
# tuitest and are left alone.
set -eu

dry_run=0
if [ "${1-}" = "-n" ]; then
	dry_run=1
	shift
fi

if [ $# -lt 1 ]; then
	echo "usage: $0 [-n] /path/to/tuios [commit]" >&2
	exit 2
fi

src=$1
ref=${2-HEAD}
dest=$(CDPATH= cd -- "$(dirname -- "$0")/../internal/vt" && pwd)

if [ ! -d "$src/.git" ]; then
	echo "$0: $src is not a git checkout of tuios" >&2
	exit 1
fi

commit=$(git -C "$src" rev-parse "$ref")
date=$(git -C "$src" log -1 --format=%ad --date=short "$commit")
recorded=$(awk '$1 == "commit" { print $2 }' "$dest/UPSTREAM" 2>/dev/null || true)

echo "upstream: $commit ($date)"
echo "recorded: ${recorded:-none}"

changed=0
files=$(git -C "$src" ls-tree --name-only "$commit" internal/vt/ | sed 's|.*/||' | grep '\.go$' | grep -v '_test\.go$')

for f in $files; do
	tmp=$(mktemp)
	git -C "$src" show "$commit:internal/vt/$f" >"$tmp"
	if [ -f "$dest/$f" ] && cmp -s "$tmp" "$dest/$f"; then
		rm -f "$tmp"
		continue
	fi
	changed=$((changed + 1))
	echo "  differs: $f"
	if [ "$dry_run" -eq 0 ]; then
		cat "$tmp" >"$dest/$f"
	fi
	rm -f "$tmp"
done

# Files that exist here but not upstream, ignoring tuitest's own additions.
for f in "$dest"/*.go; do
	b=$(basename "$f")
	case "$b" in
	doc.go | *_test.go) continue ;;
	esac
	echo "$files" | grep -qx "$b" || {
		changed=$((changed + 1))
		echo "  no longer upstream: $b (remove it by hand)"
	}
done

if [ "$dry_run" -eq 1 ]; then
	echo "$changed file(s) differ (dry run, nothing written)"
	[ "$changed" -eq 0 ] && [ "$recorded" = "$commit" ]
	exit $?
fi

cat >"$dest/UPSTREAM" <<EOF
repo https://github.com/Gaurav-Gosain/tuios
path internal/vt
commit $commit
date $date
EOF

echo "$changed file(s) updated; UPSTREAM now records $commit"
echo "next: go test -race ./... and review any golden that moves"
