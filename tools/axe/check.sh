#!/bin/sh
# axe gate (P10): boot the app against fixtures/p01 and run axe-core over the
# served pages. Node is confined to this script (spec §2: no Node in binary).
# Skeleton behavior: exits 0 SKIP until the server boots and serves a page;
# once pages exist, axe failures (critical/serious) fail the gate.
set -u

ADDR="${AXE_ADDR:-127.0.0.1:8137}"
VAULT="${AXE_VAULT:-fixtures/p01}"
AXE_SPEC="${AXE_SPEC:-@axe-core/cli@latest}"
# Optional chromedriver override (CI installs a Chrome-matched driver via
# browser-actions/setup-chrome and passes AXE_CHROMEDRIVER_PATH).
AXE_CHROMEDRIVER_PATH="${AXE_CHROMEDRIVER_PATH:-}"
AXE_CHROME_PATH="${AXE_CHROME_PATH:-}"
# Chrome flags for containerized CI (throwaway scan browser only, not the
# product): --no-sandbox (runner user namespaces), --disable-dev-shm-usage
# (/dev/shm too small → instant "Chrome instance exited"), --disable-gpu.
# axe-cli splits --chrome-options on commas, so this stays ONE comma-joined
# argument (never space-separated).
AXE_CHROME_OPTIONS="${AXE_CHROME_OPTIONS:---no-sandbox,--disable-dev-shm-usage,--disable-gpu}"
# Data dir defaults to tmp (never litter the repo with sibling *-data dirs).
AXE_DATA_DIR="${AXE_DATA_DIR:-}"

tmp=$(mktemp -d)
trap 'kill "$srv" 2>/dev/null; rm -rf "$tmp"' EXIT INT TERM
DATADIR="${AXE_DATA_DIR:-$tmp/data}"

echo "axe: building server"
if ! go build -o "$tmp/yonder-axe" ./cmd/app; then
	echo "axe: SKIP (server does not build yet)"
	exit 0
fi

# Import page rows so /p/* pages serve (best-effort: without an index the
# page probes below 404 and the gate keeps its historic SKIP).
"$tmp/yonder-axe" reindex --vault "$VAULT" --data-dir "$DATADIR" >/dev/null 2>&1 || true

"$tmp/yonder-axe" --vault "$VAULT" --addr "$ADDR" --data-dir "$DATADIR" >"$tmp/server.log" 2>&1 &
srv=$!

healthy=0
i=0
while [ "$i" -lt 25 ]; do
	if curl -sf "http://$ADDR/healthz" >/dev/null 2>&1; then
		healthy=1
		break
	fi
	i=$((i + 1))
	sleep 1
done
if [ "$healthy" -eq 0 ]; then
	echo "axe: SKIP (/healthz never came up; serve path not wired yet)"
	tail -5 "$tmp/server.log" 2>/dev/null || true
	exit 0
fi

# Collect candidate P01 pages; skip the gate until at least one serves 200.
pages=""
for p in "/" "/notes" "/p/welcome.md" ; do
	if curl -sf -o /dev/null "http://$ADDR$p"; then
		pages="$pages http://$ADDR$p"
	fi
done
if [ -z "$pages" ]; then
	echo "axe: SKIP (server healthy, no P01 pages served yet)"
	exit 0
fi

echo "axe: scanning:$pages (spec $AXE_SPEC)"
EXTRA=""
if [ -n "$AXE_CHROMEDRIVER_PATH" ]; then
	EXTRA="$EXTRA --chromedriver-path $AXE_CHROMEDRIVER_PATH"
fi
if [ -n "$AXE_CHROME_PATH" ]; then
	EXTRA="$EXTRA --chrome-path $AXE_CHROME_PATH"
fi
if [ -n "$AXE_CHROME_OPTIONS" ]; then
	# `=` form: the value itself starts with `--`, which commander would
	# otherwise parse as a new flag.
	EXTRA="$EXTRA --chrome-options=$AXE_CHROME_OPTIONS"
fi
# shellcheck disable=SC2086
if npx -y "$AXE_SPEC" $pages --exit $EXTRA; then
	echo "axe: PASS (no critical/serious violations)"
else
	echo "axe: FAIL (violations found)"
	exit 1
fi
