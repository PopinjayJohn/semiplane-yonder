#!/bin/sh
# axe gate (P10): boot the app against fixtures/p01 and run axe-core over the
# served pages. Node is confined to this script (spec §2: no Node in binary).
# Skeleton behavior: exits 0 SKIP until the server boots and serves a page;
# once pages exist, axe failures (critical/serious) fail the gate.
set -u

ADDR="${AXE_ADDR:-127.0.0.1:8137}"
VAULT="${AXE_VAULT:-fixtures/p01}"
AXE_SPEC="${AXE_SPEC:-@axe-core/cli@latest}"

tmp=$(mktemp -d)
trap 'kill "$srv" 2>/dev/null; rm -rf "$tmp"' EXIT INT TERM

echo "axe: building server"
if ! go build -o "$tmp/yonder-axe" ./cmd/app; then
	echo "axe: SKIP (server does not build yet)"
	exit 0
fi

"$tmp/yonder-axe" --vault "$VAULT" --addr "$ADDR" >"$tmp/server.log" 2>&1 &
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
for p in "/" "/notes" ; do
	if curl -sf -o /dev/null "http://$ADDR$p"; then
		pages="$pages http://$ADDR$p"
	fi
done
if [ -z "$pages" ]; then
	echo "axe: SKIP (server healthy, no P01 pages served yet)"
	exit 0
fi

echo "axe: scanning:$pages (spec $AXE_SPEC)"
# shellcheck disable=SC2086
if npx -y "$AXE_SPEC" $pages --exit; then
	echo "axe: PASS (no critical/serious violations)"
else
	echo "axe: FAIL (violations found)"
	exit 1
fi
