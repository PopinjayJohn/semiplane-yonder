#!/bin/sh
# fuzz-short: run every Go fuzz target briefly (P10 CI gate).
# Usage: make fuzz-short  |  FUZZ_TIME=60s make fuzz-short
# Exits 0 with SKIP when the tree has no fuzz targets yet.
set -u

TIME="${FUZZ_TIME:-20s}"
found=0
failed=0

for file in $(grep -rl --include='*_test.go' -E '^func (Fuzz[A-Za-z0-9_]+)' . 2>/dev/null); do
	dir=$(dirname "$file")
	for target in $(grep -o -E '^func (Fuzz[A-Za-z0-9_]+)' "$file" | awk '{print $2}' | cut -d'(' -f1); do
		found=$((found + 1))
		echo "fuzz-short: $target ($dir, $TIME)"
		if ! go test -run='^$' -fuzz="$target" -fuzztime="$TIME" "./$dir"; then
			failed=$((failed + 1))
		fi
	done
done

if [ "$found" -eq 0 ]; then
	echo "fuzz-short: SKIP (no fuzz targets in tree)"
	exit 0
fi
if [ "$failed" -ne 0 ]; then
	echo "fuzz-short: FAIL ($failed targets failed)"
	exit 1
fi
echo "fuzz-short: PASS ($found targets)"
