package stream

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"roving/backend/internal/realtime"
)

const origin = "http://localhost:4321"

// Users named "owner*" resolve to host, "blocked" is refused, everyone else is a viewer.
func resolver(_ context.Context, userID, _, _ string) (Role, error) {
	switch {
	case userID == "blocked":
		return "", errors.New("no")
	case strings.HasPrefix(userID, "owner"):
		return RoleHost, nil
	}
	return RoleViewer, nil
}

type env struct {
	hub *realtime.Hub
	reg *Registry
	srv *httptest.Server
}

func setup(t *testing.T) *env {
	hub := realtime.NewHub(realtime.Config{Logf: func(string, ...any) {}})
	reg := NewRegistry()
	Register(hub, reg, resolver)
	realtime.RegisterChat(hub, reg)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ck, err := r.Cookie("uid")
		if err != nil {
			http.Error(w, "no", 401)
			return
		}
		hub.Serve(w, r, ck.Value, ck.Value, origin)
	}))
	t.Cleanup(srv.Close)
	return &env{hub, reg, srv}
}

type peer struct {
	t    *testing.T
	c    *websocket.Conn
	id   string
	user string
}

func (e *env) connect(t *testing.T, user string) *peer {
	h := http.Header{}
	h.Set("Cookie", "uid="+user)
	h.Set("Origin", origin)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(e.srv.URL, "http"), &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.CloseNow() })
	p := &peer{t: t, c: c, user: user}
	hello := p.expect("hello")
	var hp struct{ ConnectionID string }
	json.Unmarshal(hello.Payload, &hp)
	p.id = hp.ConnectionID
	return p
}

func (p *peer) send(typ string, payload any) {
	b, _ := json.Marshal(map[string]any{"type": typ, "id": "x", "payload": payload})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := p.c.Write(ctx, websocket.MessageText, b); err != nil {
		p.t.Fatal(err)
	}
}

func (p *peer) next() realtime.Envelope {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, b, err := p.c.Read(ctx)
	if err != nil {
		p.t.Fatalf("%s read: %v", p.user, err)
	}
	var e realtime.Envelope
	json.Unmarshal(b, &e)
	return e
}

// expect skips unrelated messages until one of the wanted type arrives.
func (p *peer) expect(typ string) realtime.Envelope {
	for i := 0; i < 10; i++ {
		if e := p.next(); e.Type == typ {
			return e
		}
	}
	p.t.Fatalf("%s never got %s", p.user, typ)
	return realtime.Envelope{}
}

func (p *peer) join(room string) string {
	p.send(TypeStreamJoin, map[string]string{"groupId": "g1", "roomId": room})
	var j struct{ Role string }
	json.Unmarshal(p.expect(TypeStreamJoined).Payload, &j)
	return j.Role
}

func (p *peer) roster() (host string, n int) {
	var st struct {
		HostConnID   string
		Participants []any
	}
	json.Unmarshal(p.expect(TypeStreamState).Payload, &st)
	return st.HostConnID, len(st.Participants)
}

func TestRolesComeFromTheServer(t *testing.T) {
	e := setup(t)
	owner, viewer := e.connect(t, "owner"), e.connect(t, "alice")
	if r := owner.join("r1"); r != "host" {
		t.Fatalf("owner role = %s", r)
	}
	if r := viewer.join("r1"); r != "viewer" {
		t.Fatalf("viewer role = %s", r)
	}
	host, n := viewer.roster()
	if host != owner.id || n != 2 {
		t.Fatalf("roster host=%s n=%d", host, n)
	}
}

func TestSecondHostTabIsViewer(t *testing.T) {
	e := setup(t)
	a, b := e.connect(t, "owner"), e.connect(t, "owner")
	if a.join("r1") != "host" || b.join("r1") != "viewer" {
		t.Fatal("only one publisher allowed")
	}
}

func TestRefusedUserNeverJoins(t *testing.T) {
	e := setup(t)
	b := e.connect(t, "blocked")
	b.send(TypeStreamJoin, map[string]string{"groupId": "g1", "roomId": "r1"})
	if er := b.expect("error"); !strings.Contains(string(er.Payload), "forbidden") {
		t.Fatal("want forbidden")
	}
	if len(e.reg.RoomConnections("r1")) != 0 {
		t.Fatal("refused user is in the roster")
	}
}

func TestSignalingRules(t *testing.T) {
	e := setup(t)
	owner, v1, v2, stranger := e.connect(t, "owner"), e.connect(t, "a"), e.connect(t, "b"), e.connect(t, "s")
	owner.join("r1")
	v1.join("r1")
	v2.join("r1")

	sig := func(from *peer, to, kind string) {
		from.send(TypeStreamSignal, map[string]any{"roomId": "r1", "to": to, "kind": kind, "data": map[string]string{"sdp": "x"}})
	}
	forbidden := func(p *peer) {
		if er := p.expect("error"); !strings.Contains(string(er.Payload), "forbidden") {
			t.Fatalf("%s: want forbidden, got %s", p.user, er.Payload)
		}
	}

	// host -> viewer offer is relayed with the sender stamped by the server
	sig(owner, v1.id, "offer")
	got := v1.expect(TypeStreamSignal)
	var sp struct{ From, Kind string }
	json.Unmarshal(got.Payload, &sp)
	if sp.From != owner.id || sp.Kind != "offer" {
		t.Fatalf("bad relay: %s", got.Payload)
	}
	// viewer -> host answer works
	sig(v1, owner.id, "answer")
	owner.expect(TypeStreamSignal)
	// a viewer cannot publish
	sig(v1, owner.id, "offer")
	forbidden(v1)
	// viewers cannot talk to each other
	sig(v1, v2.id, "ice")
	forbidden(v1)
	// someone who never joined cannot signal into the room
	sig(stranger, owner.id, "ice")
	forbidden(stranger)
	// ...and cannot be signalled either
	sig(owner, stranger.id, "offer")
	forbidden(owner)
}

func TestDisconnectRemovesAndNotifies(t *testing.T) {
	e := setup(t)
	owner, v := e.connect(t, "owner"), e.connect(t, "a")
	owner.join("r1")
	v.join("r1")
	v.roster()
	owner.c.CloseNow() // no stream.leave: the tab just died
	deadline := time.Now().Add(3 * time.Second)
	for {
		host, n := v.roster()
		if host == "" && n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("host still listed: host=%q n=%d", host, n)
		}
	}
}

func TestSwitchingRoomsLeavesTheOldOne(t *testing.T) {
	e := setup(t)
	v := e.connect(t, "a")
	v.join("r1")
	v.join("r2")
	if len(e.reg.RoomConnections("r1")) != 0 || len(e.reg.RoomConnections("r2")) != 1 {
		t.Fatal("connection should only be in r2")
	}
}

func TestChatNeedsRoomMembership(t *testing.T) {
	e := setup(t)
	a, b, out := e.connect(t, "a"), e.connect(t, "b"), e.connect(t, "out")
	a.join("r1")
	b.join("r1")
	a.send(realtime.TypeChatSend, map[string]string{"roomId": "r1", "text": "hello"})
	for _, p := range []*peer{a, b} {
		if m := p.expect(realtime.TypeChatMsg); !strings.Contains(string(m.Payload), `"text":"hello"`) {
			t.Fatalf("%s: %s", p.user, m.Payload)
		}
	}
	out.send(realtime.TypeChatSend, map[string]string{"roomId": "r1", "text": "sneaky"})
	if er := out.expect("error"); !strings.Contains(string(er.Payload), "forbidden") {
		t.Fatal("outsider must not post into the room")
	}
}

func TestOnlyHostSetsCameraFlag(t *testing.T) {
	e := setup(t)
	owner, v := e.connect(t, "owner"), e.connect(t, "a")
	owner.join("r1")
	v.join("r1")
	v.send(TypeStreamMedia, map[string]any{"roomId": "r1", "camera": true}) // ignored
	owner.send(TypeStreamMedia, map[string]any{"roomId": "r1", "camera": true})
	for i := 0; i < 10; i++ {
		var st struct{ HostCamera bool }
		json.Unmarshal(v.expect(TypeStreamState).Payload, &st)
		if st.HostCamera {
			return
		}
	}
	t.Fatal("viewers never saw hostCamera=true")
}
