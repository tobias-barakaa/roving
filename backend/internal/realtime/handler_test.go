package realtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

const testOrigin = "http://localhost:4321"

// newTestServer fakes the auth layer: the "uid" cookie is the user, no cookie is a 401.
func newTestServer(t *testing.T, cfg Config) (*Hub, *httptest.Server) {
	t.Helper()
	cfg.Logf = func(string, ...any) {}
	hub := NewHub(cfg)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ck, err := r.Cookie("uid")
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		hub.Serve(w, r, ck.Value, ck.Value, testOrigin)
	}))
	t.Cleanup(srv.Close)
	return hub, srv
}

func dial(t *testing.T, srv *httptest.Server, user, origin string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	h := http.Header{}
	if user != "" {
		h.Set("Cookie", "uid="+user)
	}
	if origin != "" {
		h.Set("Origin", origin)
	}
	c, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), &websocket.DialOptions{HTTPHeader: h})
	if err == nil {
		t.Cleanup(func() { c.CloseNow() })
	}
	return c, resp, err
}

func read(t *testing.T, c *websocket.Conn) Envelope {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, b, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var e Envelope
	if err := json.Unmarshal(b, &e); err != nil {
		t.Fatal(err)
	}
	return e
}

func write(t *testing.T, c *websocket.Conn, s string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, []byte(s)); err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func closeStatus(t *testing.T, c *websocket.Conn) websocket.StatusCode {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _, err := c.Read(ctx)
	return websocket.CloseStatus(err)
}

func TestHelloAndEcho(t *testing.T) {
	_, srv := newTestServer(t, Config{})
	c, _, err := dial(t, srv, "u1", testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	hello := read(t, c)
	var hp HelloPayload
	json.Unmarshal(hello.Payload, &hp)
	if hello.Type != TypeHello || hp.UserID != "u1" || hp.ConnectionID == "" {
		t.Fatalf("bad hello: %+v %+v", hello, hp)
	}
	write(t, c, `{"type":"echo","id":"7","payload":{"text":"hi"}}`)
	e := read(t, c)
	if e.Type != TypeEcho || e.ID != "7" || !strings.Contains(string(e.Payload), `"text":"hi"`) {
		t.Fatalf("bad echo: %+v %s", e, e.Payload)
	}
	write(t, c, `{"type":"ping","id":"p"}`)
	if p := read(t, c); p.Type != TypePong || p.ID != "p" {
		t.Fatalf("bad pong: %+v", p)
	}
}

func TestBadAndUnknownMessages(t *testing.T) {
	_, srv := newTestServer(t, Config{})
	c, _, _ := dial(t, srv, "u1", testOrigin)
	read(t, c)
	for _, msg := range []string{`not json`, `{"type":"nope","id":"x"}`, `{"type":"echo","payload":{}}`} {
		write(t, c, msg)
		if e := read(t, c); e.Type != TypeError {
			t.Fatalf("%s -> %+v", msg, e)
		}
	}
	write(t, c, `{"type":"ping"}`) // still alive
	if e := read(t, c); e.Type != TypePong {
		t.Fatal("connection should survive bad messages")
	}
}

func TestUnauthenticatedRejected(t *testing.T) {
	_, srv := newTestServer(t, Config{})
	_, resp, err := dial(t, srv, "", testOrigin)
	if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401, got %v %v", resp, err)
	}
}

func TestBadOriginRejected(t *testing.T) {
	_, srv := newTestServer(t, Config{})
	_, resp, err := dial(t, srv, "u1", "https://evil.example")
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403, got %v %v", resp, err)
	}
}

func TestPerUserCapAndCleanup(t *testing.T) {
	hub, srv := newTestServer(t, Config{MaxConnsPerUser: 2})
	a, _, _ := dial(t, srv, "u1", testOrigin)
	b, _, _ := dial(t, srv, "u1", testOrigin)
	read(t, a)
	read(t, b)
	_, resp, err := dial(t, srv, "u1", testOrigin)
	if err == nil || resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("want 429, got %v %v", resp, err)
	}
	if _, _, err := dial(t, srv, "u2", testOrigin); err != nil {
		t.Fatal("other users unaffected:", err)
	}
	if n := hub.UserConnections("u1"); n != 2 {
		t.Fatalf("u1 conns = %d", n)
	}
	a.Close(websocket.StatusNormalClosure, "bye")
	waitFor(t, "cleanup", func() bool { return hub.UserConnections("u1") == 1 })
	b.Close(websocket.StatusNormalClosure, "bye")
	waitFor(t, "user removed", func() bool { return hub.UserConnections("u1") == 0 })
}

func TestFanoutToOtherTabs(t *testing.T) {
	_, srv := newTestServer(t, Config{})
	a, _, _ := dial(t, srv, "u1", testOrigin)
	b, _, _ := dial(t, srv, "u1", testOrigin)
	o, _, _ := dial(t, srv, "u2", testOrigin)
	read(t, a)
	read(t, b)
	read(t, o)
	write(t, a, `{"type":"echo","payload":{"text":"all","toAllMyTabs":true}}`)
	read(t, a) // own reply
	if e := read(t, b); e.Type != TypeEcho || !strings.Contains(string(e.Payload), `"fanout":true`) {
		t.Fatalf("tab b: %+v", e)
	}
	write(t, o, `{"type":"ping"}`)
	if e := read(t, o); e.Type != TypePong {
		t.Fatalf("other user got %+v (must not receive u1's echo)", e)
	}
}

func TestRateLimitCloses(t *testing.T) {
	_, srv := newTestServer(t, Config{RatePerSecond: 1, RateBurst: 3})
	c, _, _ := dial(t, srv, "u1", testOrigin)
	read(t, c)
	for i := 0; i < 10; i++ {
		write(t, c, `{"type":"ping"}`)
	}
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, _, err := c.Read(ctx)
		cancel()
		if err != nil {
			if s := websocket.CloseStatus(err); s != websocket.StatusPolicyViolation {
				t.Fatalf("close status = %v (%v)", s, err)
			}
			return
		}
	}
}

func TestOversizeMessageCloses(t *testing.T) {
	_, srv := newTestServer(t, Config{MaxMessageBytes: 256})
	c, _, _ := dial(t, srv, "u1", testOrigin)
	read(t, c)
	write(t, c, `{"type":"echo","payload":{"text":"`+strings.Repeat("a", 500)+`"}}`)
	if s := closeStatus(t, c); s != websocket.StatusMessageTooBig {
		t.Fatalf("status = %v", s)
	}
}

func TestReauthClosesEndedSession(t *testing.T) {
	var dead atomic.Bool
	hub, srv := newTestServer(t, Config{
		ReauthEvery: 50 * time.Millisecond,
		Reauth: func(_ context.Context, tok string) (string, error) {
			if dead.Load() {
				return "", ErrSessionInvalid
			}
			return tok, nil
		},
	})
	c, _, _ := dial(t, srv, "u1", testOrigin)
	read(t, c)
	time.Sleep(150 * time.Millisecond)
	write(t, c, `{"type":"ping"}`)
	if e := read(t, c); e.Type != TypePong {
		t.Fatal("valid session should stay connected")
	}
	dead.Store(true)
	if s := closeStatus(t, c); s != websocket.StatusPolicyViolation {
		t.Fatalf("status = %v", s)
	}
	waitFor(t, "cleanup", func() bool { return hub.Stats().Connections == 0 })
}

func TestReauthTemporaryErrorKeepsConnection(t *testing.T) {
	_, srv := newTestServer(t, Config{
		ReauthEvery: 30 * time.Millisecond,
		Reauth:      func(context.Context, string) (string, error) { return "", context.DeadlineExceeded },
	})
	c, _, _ := dial(t, srv, "u1", testOrigin)
	read(t, c)
	time.Sleep(150 * time.Millisecond)
	write(t, c, `{"type":"ping"}`)
	if e := read(t, c); e.Type != TypePong {
		t.Fatal("db hiccup must not log people out")
	}
}

func TestDisconnectUser(t *testing.T) {
	hub, srv := newTestServer(t, Config{})
	a, _, _ := dial(t, srv, "u1", testOrigin)
	b, _, _ := dial(t, srv, "u1", testOrigin)
	read(t, a)
	read(t, b)
	hub.DisconnectUser("u1", websocket.StatusPolicyViolation, "logged out")
	if closeStatus(t, a) != websocket.StatusPolicyViolation || closeStatus(t, b) != websocket.StatusPolicyViolation {
		t.Fatal("both tabs should close")
	}
	waitFor(t, "cleanup", func() bool { return hub.Stats().Connections == 0 })
}

func TestShutdown(t *testing.T) {
	hub, srv := newTestServer(t, Config{})
	c, _, _ := dial(t, srv, "u1", testOrigin)
	read(t, c)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hub.Shutdown(ctx)
	if s := closeStatus(t, c); s != websocket.StatusGoingAway {
		t.Fatalf("status = %v", s)
	}
	if hub.Stats().Connections != 0 {
		t.Fatal("connections left after shutdown")
	}
	if _, resp, err := dial(t, srv, "u1", testOrigin); err == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("new connections must be refused: %v %v", resp, err)
	}
}

func TestSlowConsumerDropped(t *testing.T) {
	hub, srv := newTestServer(t, Config{SendBuffer: 2})
	c, _, _ := dial(t, srv, "u1", testOrigin)
	read(t, c) // hello; now stop reading
	for i := 0; i < 5000 && hub.Stats().Connections > 0; i++ {
		hub.SendToUser("u1", Envelope{Type: TypeEcho, Payload: payloadOf(map[string]string{"text": strings.Repeat("x", 4096)})}, "")
		if i%50 == 0 {
			time.Sleep(time.Millisecond)
		}
	}
	waitFor(t, "slow consumer removed", func() bool { return hub.Stats().Connections == 0 })
}
