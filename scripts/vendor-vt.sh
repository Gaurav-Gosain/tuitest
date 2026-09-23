#!/usr/bin/env sh
# vendor-vt.sh re-syncs internal/vt from a tuios checkout and records the exact
# upstream commit in internal/vt/UPSTREAM. See internal/vt/VENDOR.md.
#
# Usage:
#   scripts/vendor-vt.sh [-n] /path/to/tuios [commit]
#
#   -n   report drift and change nothing.
#
# What it copies from tuios's internal/vt at the commit:
#
#   - Every non-test .go file, except the libghostty backend (files whose build
#     constraint is exactly "ghostty"). That backend needs cgo and a pinned
#     native library, and tuitest only ever builds the pure Go emulator.
#     A file listed in DIVERGENCE is not overwritten: the upstream change since
#     the recorded commit is merged into it with git merge-file, and a conflict
#     is left in the file with markers for a person to resolve.
#   - Every test file that does not need tuios's internal/testutil, with the
#     import paths of vt and fuzz/vtgen rewritten to tuitest's. This carries the
#     conformance corpus and the fuzz targets across.
#   - Everything under testdata/. Local files there, such as fuzz corpus
#     entries found in tuitest, are left alone.
#
# Files that belong to tuitest are never touched: doc.go, UPSTREAM, VENDOR.md,
# DIVERGENCE, and every file named tuitest_*.go.
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
dest=$(CDPATH='' cd -- "$(dirname -- "$0")/../internal/vt" && pwd)

if ! git -C "$src" rev-parse --git-dir >/dev/null 2>&1; then
	echo "$0: $src is not a git checkout of tuios" >&2
	exit 1
fi

commit=$(git -C "$src" rev-parse "$ref^{commit}")
date=$(git -C "$src" log -1 --format=%ad --date=short "$commit")
recorded=$(awk '$1 == "commit" { print $2 }' "$dest/UPSTREAM" 2>/dev/null || true)

echo "upstream: $commit ($date)"
echo "recorded: ${recorded:-none}"

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT INT TERM

# Export both trees once instead of running git show per file.
mkdir -p "$tmpdir/new" "$tmpdir/old"
git -C "$src" archive "$commit" internal/vt | tar -x -C "$tmpdir/new"
if [ -n "$recorded" ]; then
	git -C "$src" archive "$recorded" internal/vt | tar -x -C "$tmpdir/old"
fi
new=$tmpdir/new/internal/vt
old=$tmpdir/old/internal/vt

diverged=$(grep -v '^#' "$dest/DIVERGENCE" 2>/dev/null | sed '/^[[:space:]]*$/d' || true)

is_ghostty_only() {
	head -n 5 "$1" | grep -qx '//go:build ghostty'
}

changed=0
conflicts=0

# install copies $1 over $dest/$2 when they differ, and reports it.
install_file() {
	if [ -f "$dest/$2" ] && cmp -s "$1" "$dest/$2"; then
		return 0
	fi
	changed=$((changed + 1))
	echo "  differs: $2"
	if [ "$dry_run" -eq 0 ]; then
		mkdir -p "$(dirname -- "$dest/$2")"
		cat "$1" >"$dest/$2"
	fi
}

# rewrite gives a copied test tuitest's import paths. The drift check in
# tuitest_vendor_test.go applies the same mapping.
rewrite() {
	sed -e 's|github.com/Gaurav-Gosain/tuios/internal/fuzz/vtgen|github.com/Gaurav-Gosain/tuitest/fuzz/vtgen|' \
		-e 's|github.com/Gaurav-Gosain/tuios/internal/vt|github.com/Gaurav-Gosain/tuitest/internal/vt|' \
		"$1" >"$2"
}

upstream_files=""
for path in "$new"/*.go; do
	f=$(basename -- "$path")
	is_ghostty_only "$path" && continue
	theirs=$path
	base=$old/$f
	case "$f" in
	tuitest_*) continue ;;
	*_test.go)
		grep -q 'tuios/internal/testutil' "$path" && continue
		rewrite "$path" "$tmpdir/theirs"
		theirs=$tmpdir/theirs
		if [ -f "$old/$f" ]; then
			rewrite "$old/$f" "$tmpdir/base"
			base=$tmpdir/base
		fi
		;;
	esac
	upstream_files="$upstream_files $f"
	if ! echo "$diverged" | grep -qx "$f"; then
		install_file "$theirs" "$f"
		continue
	fi
	# A listed file keeps its local changes: merge only what upstream changed
	# between the recorded commit and this one.
	if [ ! -f "$base" ]; then
		echo "  DIVERGENCE lists $f but it is not in the recorded commit; merge it by hand"
		conflicts=$((conflicts + 1))
		continue
	fi
	cmp -s "$base" "$theirs" && continue
	cp "$dest/$f" "$tmpdir/ours"
	if git merge-file -q "$tmpdir/ours" "$base" "$theirs"; then
		cmp -s "$tmpdir/ours" "$dest/$f" && continue
		echo "  merged: $f (listed in DIVERGENCE)"
	else
		echo "  CONFLICT: $f (listed in DIVERGENCE); resolve the markers"
		conflicts=$((conflicts + 1))
	fi
	changed=$((changed + 1))
	if [ "$dry_run" -eq 0 ]; then
		cat "$tmpdir/ours" >"$dest/$f"
	fi
done

# testdata is copied file by file so local additions survive.
if [ -d "$new/testdata" ]; then
	(cd "$new" && find testdata -type f) >"$tmpdir/testdata"
	while IFS= read -r rel; do
		install_file "$new/$rel" "$rel"
	done <"$tmpdir/testdata"
fi

# Files here that upstream no longer has, ignoring tuitest's own.
for path in "$dest"/*.go; do
	f=$(basename -- "$path")
	case "$f" in
	doc.go | tuitest_*) continue ;;
	esac
	case " $upstream_files " in
	*" $f "*) ;;
	*)
		changed=$((changed + 1))
		echo "  no longer upstream: $f (remove it by hand)"
		;;
	esac
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
if [ "$conflicts" -gt 0 ]; then
	echo "$conflicts file(s) need a hand merge before this builds"
	exit 1
fi
echo "next: gofmt -l ., go test -race ./..., and review any golden that moves"
