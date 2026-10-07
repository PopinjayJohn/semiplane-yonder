#!/bin/sh
# serve-smoke (G2 amend, Lane E2; R1 amend removed the smokeseed crutch):
# serve-after-init smoke over HTTP against a temp vault. Fails the build
# when serve crashes or any assertion fails.
#
# Flow: fresh `init --bare` into a temp dir, seed one public + one secret
# page, `reindex` (which imports page rows for real via the F1-owned
# web.ParsePage adapter — no throwaway seed helpers), boot `serve` on a
# test port, assert the M1 guest/owner/GM matrix over HTTP, then shut down.
#
# Assertions (byte-level where the leak matrix demands it):
#   guest 200 on non-secret (no secret title/content in body)
#   guest 404 on secret, byte-identical to missing-page 404, no secret words
#   other-player 404 on secret
#   owner 200 on own secret (sees `-` block content)
#   GM POST /events 202
#   per-viewer SSE hello: guest visible:false with no secret title,
#     owner visible:true with the title
#
# Usage: ./tools/serve-smoke.sh   (from the repo root)
# Env: SMOKE_ADDR (default 127.0.0.1:18171), SMOKE_KEEP (set to keep the
# temp dir for debugging; prints its path).
set -u

ADDR="${SMOKE_ADDR:-127.0.0.1:18171}"
BASE="http://$ADDR"
TITLE="Smoketest Pact of Embers ZZ9"
OWNER="owen"

tmp=$(mktemp -d)
srv=""
failures=0

cleanup() {
	if [ -n "$srv" ]; then
		kill "$srv" 2>/dev/null
	fi
	if [ -z "${SMOKE_KEEP:-}" ]; then
		rm -rf "$tmp"
	else
		echo "smoke: kept $tmp"
	fi
}
trap 'cleanup' EXIT INT TERM

fail() {
	failures=$((failures + 1))
	echo "smoke: FAIL $1"
}

pass() {
	echo "smoke: PASS $1"
}

echo "smoke: building server"
if ! go build -o "$tmp/yonder-smoke" ./cmd/app; then
	fail "server does not build"
	exit 1
fi

VAULT="$tmp/smokevault"
echo "smoke: init --bare"
if ! "$tmp/yonder-smoke" init --vault "$VAULT" --bare \
	--gm-user gm --gm-password 'smoke-gm-pass-1' >/dev/null 2>&1; then
	fail "init --bare"
	exit 1
fi

cat >"$VAULT/welcome.md" <<'EOF'
---
title: Smoke Meadow
---

A sunny public field. Crows dream here.
EOF
cat >"$VAULT/pact.md" <<EOF
---
title: $TITLE
secret: true
owner: $OWNER
---

Signed at midnight under the third flagstone.
EOF

echo "smoke: reindex (imports page rows via web.ParsePage)"
if ! "$tmp/yonder-smoke" reindex --vault "$VAULT" >/dev/null 2>&1; then
	fail "reindex"
	exit 1
fi
INDEX="$tmp/smokevault-data/smokevault.index.db"
if [ ! -f "$INDEX" ]; then
	fail "index db missing at $INDEX"
	exit 1
fi

echo "smoke: boot serve on $ADDR"
"$tmp/yonder-smoke" serve --vault "$VAULT" --addr "$ADDR" >"$tmp/serve.log" 2>&1 &
srv=$!

healthy=0
i=0
while [ "$i" -lt 25 ]; do
	if curl -sf "$BASE/healthz" >/dev/null 2>&1; then
		healthy=1
		break
	fi
	if ! kill -0 "$srv" 2>/dev/null; then
		break
	fi
	i=$((i + 1))
	sleep 1
done
if [ "$healthy" != "1" ]; then
	fail "serve did not become healthy (see serve.log tail below)"
	tail -20 "$tmp/serve.log"
	kill "$srv" 2>/dev/null
	exit 1
fi
pass "serve boots after init --bare"

code() { # code <outfile> <url...>: prints HTTP status, saves body
	out="$1"
	shift
	curl -s -o "$out" -w "%{http_code}" "$@"
}

# 1. Guest reads non-secret.
if [ "$(code "$tmp/pub.html" "$BASE/p/welcome.md")" != "200" ]; then
	fail "guest public page status"
else
	pass "guest 200 on non-secret"
fi
if grep -qi "embers\|flagstone\|zz9" "$tmp/pub.html"; then
	fail "guest public page leaked secret words"
else
	pass "guest public page carries no secret words"
fi

# 2. Guest blocked on secret: 404 byte-identical to missing-page 404.
sc=$(code "$tmp/sec.html" "$BASE/p/pact.md")
mc=$(code "$tmp/mis.html" "$BASE/p/no-such-page-zz9.md")
if [ "$sc" != "404" ] || [ "$mc" != "404" ]; then
	fail "secret/missing statuses ($sc/$mc), want 404/404"
else
	pass "guest 404 on secret and missing"
fi
if cmp -s "$tmp/sec.html" "$tmp/mis.html"; then
	pass "secret 404 byte-identical to missing 404"
else
	fail "secret 404 differs from missing 404 (oracle)"
fi
if grep -qi "embers\|flagstone\|$OWNER\|smoketest pact" "$tmp/sec.html"; then
	fail "secret 404 leaked secret words"
else
	pass "secret 404 carries no secret words"
fi

# 3. Other player blocked too.
if [ "$(code "$tmp/other.html" "$BASE/p/pact.md?as=cass")" != "404" ]; then
	fail "other-player secret status, want 404"
else
	pass "other-player 404 on secret"
fi

# 4. Owner reads own secret.
if [ "$(code "$tmp/own.html" "$BASE/p/pact.md?as=$OWNER")" != "200" ]; then
	fail "owner secret status, want 200"
else
	pass "owner 200 on own secret"
fi
if grep -q "third flagstone" "$tmp/own.html"; then
	pass "owner sees secret block content"
else
	fail "owner missing secret block content"
fi

# 5. GM broadcast.
if [ "$(curl -s -o /dev/null -w '%{http_code}' -X POST \
	--data-urlencode 'path=pact.md' "$BASE/events?as=gm")" != "202" ]; then
	fail "GM POST /events, want 202"
else
	pass "GM POST /events 202"
fi

# 6. Per-viewer SSE hello (hello flushes immediately; max-time bounds the stream).
curl -sN --max-time 4 -o "$tmp/sse_guest.txt" "$BASE/events?path=pact.md" 2>/dev/null || [ $? -eq 28 ]
curl -sN --max-time 4 -o "$tmp/sse_owner.txt" "$BASE/events?path=pact.md&as=$OWNER" 2>/dev/null || [ $? -eq 28 ]
if grep -q "event: hello" "$tmp/sse_guest.txt" && grep -q '"visible":false' "$tmp/sse_guest.txt"; then
	pass "guest SSE hello (not visible)"
else
	fail "guest SSE hello missing/not-hidden"
fi
if grep -qi "embers\|smoketest pact" "$tmp/sse_guest.txt"; then
	fail "guest SSE hello leaked secret title"
else
	pass "guest SSE hello carries no secret title"
fi
if grep -q '"visible":true' "$tmp/sse_owner.txt" && grep -q "$TITLE" "$tmp/sse_owner.txt"; then
	pass "owner SSE hello (visible, titled)"
else
	fail "owner SSE hello missing title/visible"
fi

# 7. Serve still alive at the end.
if kill -0 "$srv" 2>/dev/null; then
	pass "serve alive after matrix"
else
	fail "serve died mid-matrix (see serve.log tail below)"
	tail -20 "$tmp/serve.log"
fi

if [ "$failures" != "0" ]; then
	echo "smoke: $failures failure(s)"
	exit 1
fi
echo "smoke: all assertions passed"
