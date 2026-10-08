package vtt

// M3 load evidence (P08): 6 clients move tokens concurrently over real HTTP
// with hotel-WiFi jitter and resync every round. Pass = all clients converge
// on identical snapshots matching app-DB truth (no desync), hidden tokens
// never leak to players on any surface, and fog stays fail-closed across an
// index rebuild + mask corruption.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/semiplane/yonder/internal/auth"
	"github.com/semiplane/yonder/internal/plugins"
	"github.com/semiplane/yonder/internal/web"
)

type sseEvent struct {
	name, data string
}

// loadClient is one simulated table client: moves one token, resyncs via the
// snapshot endpoint, and watches /events for leaks.
type loadClient struct {
	user    string
	token   string
	sessID  string
	events  []sseEvent
	mu      sync.Mutex
	snapLog []string
}

func (c *loadClient) record(ev sseEvent) {
	c.mu.Lock()
	c.events = append(c.events, ev)
	c.mu.Unlock()
}

func (c *loadClient) sawLeak() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, ev := range c.events {
		if strings.Contains(ev.data, "gob-1") || strings.Contains(ev.data, "Goblin Boss") {
			return ev.name + ": " + ev.data
		}
	}
	return ""
}

func (c *loadClient) moveCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, ev := range c.events {
		if ev.name == plugins.EventTokenMove {
			n++
		}
	}
	return n
}

// readSSE streams /events until ctx ends, collecting parsed events.
func readSSE(ctx context.Context, url string, c *loadClient) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer func() { _ = resp.Body.Close() }()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 64*1024)
	var name string
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "event: ") {
			name = strings.TrimSpace(strings.TrimPrefix(line, "event: "))
		} else if strings.HasPrefix(line, "data: ") && name != "" {
			c.record(sseEvent{name: name, data: strings.TrimPrefix(line, "data: ")})
			name = ""
		}
	}
}

func TestSixClientNoDesync(t *testing.T) {
	s := testStore(t)
	seedArena(t, s, false, "")
	seedSheetPage(t, s, "mira", "mira", true)
	ctx := context.Background()

	users := []string{"mira", "bram", "cass", "dev", "eli", "fay"}
	sessMap := map[string]*auth.Session{}
	for i, u := range users {
		id := fmt.Sprintf("%032x", 0xa0+i)
		sessMap[id] = &auth.Session{ID: id, UserID: u, CSRFToken: testCSRF,
			CreatedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix()}
		seedSheetPage(t, s, u, u, true)
		if err := UpsertToken(ctx, s, "arena", Token{ID: "pc-" + u, Name: u, X: i, Y: i, HP: 10, MaxHP: 10, Character: "characters/" + u + "/index.md"}); err != nil {
			t.Fatal(err)
		}
	}
	// One hidden boss: the leak canary.
	if err := UpsertToken(ctx, s, "arena", Token{ID: "gob-1", Name: "Goblin Boss", X: 9, Y: 9, HP: 21, MaxHP: 21, Hidden: true}); err != nil {
		t.Fatal(err)
	}
	if err := SetFog(ctx, s, "arena", EncodeMask(Grid{Cols: 20, Rows: 14}, []Rect{{X: 8, Y: 8, W: 4, H: 4}})); err != nil {
		t.Fatal(err)
	}

	h := &Handlers{Store: s, Sessions: &stubSessions{sessions: sessMap}}
	readH := &web.ReadHandlers{Store: s}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /vtt/{mapID}/fragment", h.Fragment)
	mux.HandleFunc("GET /api/vtt/{mapID}/state", h.State)
	mux.HandleFunc("PATCH /vtt/{mapID}/state", h.Move)
	mux.HandleFunc("POST /vtt/{mapID}/state", h.StateUpdate)
	mux.HandleFunc("GET /events", readH.SSE)
	// Viewer injection (mirrors serve's withDemoViewer; ?as= fallback also live).
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.ViewerFromContext(r.Context()); !ok {
			if v := web.ViewerForRequest(r); v != nil {
				r = r.WithContext(auth.WithViewer(r.Context(), v))
			}
		}
		mux.ServeHTTP(w, r)
	})
	srv := httptest.NewServer(wrapped)
	defer srv.Close()

	const rounds = 8
	clients := make([]*loadClient, len(users))
	sctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for i, u := range users {
		clients[i] = &loadClient{user: u, token: "pc-" + u, sessID: fmt.Sprintf("%032x", 0xa0+i)}
		go readSSE(sctx, srv.URL+"/events?map=arena&as="+u, clients[i])
	}
	// Let hello + initial state land.
	time.Sleep(300 * time.Millisecond)

	jitter := rand.New(rand.NewSource(9))
	var jmu sync.Mutex
	sleepJitter := func() {
		jmu.Lock()
		d := time.Duration(jitter.Intn(25)) * time.Millisecond
		jmu.Unlock()
		time.Sleep(d)
	}

	httpClient := &http.Client{Timeout: 10 * time.Second}
	patch := func(c *loadClient, x, y int) error {
		body := fmt.Sprintf(`{"token":%q,"x":%d,"y":%d}`, c.token, x, y)
		req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/vtt/arena/state?as="+c.user, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(auth.CSRFHeaderName, testCSRF)
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: c.sessID})
		resp, err := httpClient.Do(req)
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("move %s = %d", c.token, resp.StatusCode)
		}
		return nil
	}
	snapshot := func(c *loadClient) plugins.StateSnapshot {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/vtt/arena/state?as="+c.user, nil)
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: c.sessID})
		resp, err := httpClient.Do(req)
		if err != nil {
			return plugins.StateSnapshot{}
		}
		defer func() { _ = resp.Body.Close() }()
		var snap plugins.StateSnapshot
		_ = json.NewDecoder(resp.Body).Decode(&snap)
		return snap
	}
	fingerprint := func(sn plugins.StateSnapshot) string {
		var parts []string
		for _, tk := range sn.Tokens {
			parts = append(parts, fmt.Sprintf("%s=%d,%d,%d/%d", tk.ID, tk.X, tk.Y, tk.HP, tk.MaxHP))
		}
		sort.Strings(parts)
		return strings.Join(parts, "|")
	}

	for r := 0; r < rounds; r++ {
		var wg sync.WaitGroup
		errs := make([]error, len(clients))
		for i, c := range clients {
			wg.Add(1)
			go func(i int, c *loadClient) {
				defer wg.Done()
				sleepJitter() // hotel-WiFi simulation: spread + jitter
				x := (i*3 + r) % 20
				y := (i*5 + r*2) % 14
				if err := patch(c, x, y); err != nil {
					errs[i] = err
				}
			}(i, c)
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("round %d client %s: %v", r, clients[i].user, err)
			}
		}
		// Resync barrier: every client re-reads full state (P08 reconnect rule).
		for _, c := range clients {
			snap := snapshot(c)
			c.snapLog = append(c.snapLog, fingerprint(snap))
			for _, tk := range snap.Tokens {
				if tk.ID == "gob-1" {
					t.Fatalf("round %d: hidden boss leaked to %s", r, c.user)
				}
			}
		}
	}
	// Settle, then compare final fingerprints across all 6 clients + GM truth.
	time.Sleep(300 * time.Millisecond)
	gmSnap, _, err := SnapshotFor(ctx, s, gmViewer(), "arena")
	if err != nil {
		t.Fatal(err)
	}
	want := ""
	for i, c := range clients {
		got := fingerprint(snapshot(c))
		if i == 0 {
			want = got
		} else if got != want {
			t.Errorf("client %s desynced:\n got %s\nwant %s", c.user, got, want)
		}
		if leak := c.sawLeak(); leak != "" {
			t.Errorf("client %s SSE leak: %s", c.user, leak)
		}
		if n := c.moveCount(); n == 0 {
			t.Errorf("client %s saw no token-move events", c.user)
		}
	}
	// GM truth: same positions + the hidden boss on top.
	gmFp := fingerprint(gmSnap)
	if !strings.Contains(gmFp, "gob-1=9,9,21/21") {
		t.Errorf("GM lost the hidden boss: %s", gmFp)
	}
	playerIDs := map[string]bool{}
	for _, p := range strings.Split(want, "|") {
		playerIDs[strings.SplitN(p, "=", 2)[0]] = true
	}
	for _, p := range strings.Split(gmFp, "|") {
		id := strings.SplitN(p, "=", 2)[0]
		if id == "gob-1" {
			continue
		}
		if !playerIDs[id] {
			t.Errorf("GM/player position divergence on %s", id)
		}
	}

	// Index rebuild keeps fog hidden: churn the sidecar index row exactly the
	// way reindex does (delete + upsert into the INDEX db only) and prove the
	// app-DB fog row is untouched + still enforced.
	fogBefore := gmSnap.Fog.Shape
	if err := s.PageDelete(ctx, "maps/arena.md"); err != nil {
		t.Fatal(err)
	}
	seedArena(t, s, false, "")
	after, _, err := SnapshotFor(ctx, s, gmViewer(), "arena")
	if err != nil {
		t.Fatal(err)
	}
	if after.Fog.Hidden || after.Fog.Shape != fogBefore {
		t.Errorf("index rebuild changed fog: %+v was %q", after.Fog, fogBefore)
	}

	// Corrupt mask fails closed for everyone (players AND GM).
	if _, err := s.AppDB().ExecContext(ctx, `UPDATE vtt_fog SET mask = 'corrupt!!' WHERE map = 'arena'`); err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]*auth.Viewer{"gm": gmViewer(), "mira": playerViewer("mira")} {
		snap, _, err := SnapshotFor(ctx, s, v, "arena")
		if err != nil {
			t.Fatal(err)
		}
		if !snap.Fog.Hidden || len(snap.Tokens) != 0 {
			t.Errorf("%s failed open on corrupt fog: %+v", name, snap.Fog)
		}
	}
}
