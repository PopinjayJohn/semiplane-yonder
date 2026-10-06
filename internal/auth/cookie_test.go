package auth

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func testKeys() [][32]byte {
	k, err := GenerateKey()
	if err != nil {
		panic(err)
	}
	return [][32]byte{k}
}

func TestSessionIDFormat(t *testing.T) {
	id, err := NewSessionID()
	if err != nil {
		t.Fatalf("new id: %v", err)
	}
	if len(id) != 32 || !ValidateSessionID(id) {
		t.Fatalf("bad generated id: %q", id)
	}
	// Traversal / injection payloads must never validate as session ids.
	for _, evil := range []string{
		"", "../auth_sessions", "..", "/", "a", strings.Repeat("a", 31),
		strings.Repeat("a", 33), "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz",
		"ab' OR '1'='1", "%2e%2e/%2e%2e", "deadbeef; DROP TABLE",
	} {
		if ValidateSessionID(evil) {
			t.Fatalf("evil id accepted: %q", evil)
		}
	}
}

func TestCookieSignVerifyRoundTrip(t *testing.T) {
	keys := testKeys()
	id, _ := NewSessionID()
	exp := time.Now().Add(time.Hour).Unix()
	v := SignSessionCookie(keys[0], id, "alice", exp)
	gotID, gotUser, gotExp, err := ParseAndVerifySessionCookie(keys, v)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if gotID != id || gotUser != "alice" || gotExp != exp {
		t.Fatalf("roundtrip mismatch: %q %q %d", gotID, gotUser, gotExp)
	}
}

func TestCookieTamperRejected(t *testing.T) {
	keys := testKeys()
	other, _ := GenerateKey()
	id, _ := NewSessionID()
	exp := time.Now().Add(time.Hour).Unix()
	v := SignSessionCookie(keys[0], id, "alice", exp)

	cases := map[string]string{
		"wrong key":      mustSign(other, id, "alice", exp),
		"swapped user":   swapUser(v, "mallory"),
		"extended":       swapExpiry(v, exp+99999),
		"truncated":      v[:len(v)-4],
		"appended":       v + "x",
		"empty":          "",
		"no pipes":       "hello",
		"too many pipes": v + "|extra",
		"bad id":         "nothexid!|alice|123|abcd",
		"bad expiry":     id + "|alice|never|abcd",
		"zero expiry":    id + "|alice|0|abcd",
		"empty user":     id + "||123|abcd",
		"traversal id":   "../../etc/passwd|alice|123|abcd",
	}
	for name, val := range cases {
		if _, _, _, err := ParseAndVerifySessionCookie(keys, val); err == nil {
			t.Fatalf("%s: tampered cookie accepted", name)
		}
	}
}

func mustSign(k [32]byte, id, user string, exp int64) string {
	return SignSessionCookie(k, id, user, exp)
}

func swapUser(v, user string) string {
	parts := strings.Split(v, "|")
	parts[1] = user
	return strings.Join(parts, "|")
}

func swapExpiry(v string, exp int64) string {
	parts := strings.Split(v, "|")
	parts[2] = strconv.FormatInt(exp, 10)
	return strings.Join(parts, "|")
}

func TestBuildSessionCookieAttrs(t *testing.T) {
	exp := time.Now().Add(time.Hour).Round(0)
	c := BuildSessionCookie("v", exp, true)
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/" {
		t.Fatalf("bad attrs: %+v", c)
	}
	plain := BuildSessionCookie("v", exp, false)
	if plain.Secure {
		t.Fatal("Secure must be conditional (off on plain LAN)")
	}
	cleared := ClearSessionCookie(false)
	if cleared.MaxAge != -1 || cleared.Value != "" {
		t.Fatalf("bad clear cookie: %+v", cleared)
	}
}

func TestCSRF(t *testing.T) {
	a, err := NewCSRFToken()
	if err != nil {
		t.Fatalf("csrf: %v", err)
	}
	b, err := NewCSRFToken()
	if err != nil {
		t.Fatalf("csrf: %v", err)
	}
	if a == b {
		t.Fatal("csrf tokens must differ")
	}
	if !ValidateCSRFToken(a, a) {
		t.Fatal("matching tokens must validate")
	}
	if ValidateCSRFToken(a, b) {
		t.Fatal("mismatched tokens must fail")
	}
	for _, tc := range [][2]string{{"", ""}, {a, ""}, {"", a}} {
		if ValidateCSRFToken(tc[0], tc[1]) {
			t.Fatalf("empty token must fail: %q %q", tc[0], tc[1])
		}
	}
}
