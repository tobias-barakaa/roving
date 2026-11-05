// internal/realtime/calls_test.go
package realtime

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

const (
	idGroup = "00000000-0000-0000-0000-0000000000a0"
	idAnn   = "00000000-0000-0000-0000-0000000000a1"
	idBen   = "00000000-0000-0000-0000-0000000000b2"
	idCy    = "00000000-0000-0000-0000-0000000000c3"
)

// newTestClient builds a connection with no socket behind it; messages the hub
// sends to it can be read back from its send channel.
func newTestClient(h *Hub, userID, name string) *Client {
	c := &Client{
		hub:         h,
		userID:      userID,
		displayName: name,
		send:        make(chan []byte, 256),
		done:        make(chan struct{}),
		subs:        make(map[string]struct{}),
		calls:       make(map[string]struct{}),
		typingAt:    make(map[string]time.Time),
	}
	h.register(c)
	return c
}

func drain(c *Client) []map[string]any {
	var out []map[string]any
	for {
		select {
		case b := <-c.send:
			var m map[string]any
			if err := json.Unmarshal(b, &m); err == nil {
				out = append(out, m)
			}
		default:
			return out
		}
	}
}

func find(msgs []map[string]any, typ string) map[string]any {
	for _, m := range msgs {
		if m["type"] == typ {
			return m
		}
	}
	return nil
}

func errCode(err error) string {
	var ce *callError
	if errors.As(err, &ce) {
		return ce.code
	}
	return ""
}

func TestCallLifecycle(t *testing.T) {
	h := NewHub(nil)
	ann, ben := newTestClient(h, idAnn, "Ann"), newTestClient(h, idBen, "Ben")
	ref := ChannelRef{GroupID: idGroup}
	for _, c := range []*Client{ann, ben} {
		if _, err := h.subscribe(c, ref); err != nil {
			t.Fatal(err)
		}
	}

	parts, fresh, err := h.joinCall(ann, ref, true, false)
	if err != nil || !fresh || len(parts) != 1 {
		t.Fatalf("ann join: parts=%d fresh=%v err=%v", len(parts), fresh, err)
	}
	h.announceJoin(ann, ref, true, false)
	parts, fresh, err = h.joinCall(ben, ref, true, true)
	if err != nil || !fresh || len(parts) != 2 {
		t.Fatalf("ben join: parts=%d fresh=%v err=%v", len(parts), fresh, err)
	}
	// Joining again from the same connection is a no-op, not an error.
	if _, fresh, err = h.joinCall(ben, ref, true, true); err != nil || fresh {
		t.Fatalf("rejoin: fresh=%v err=%v", fresh, err)
	}
	drain(ann)
	drain(ben)

	// An offer reaches only the addressed peer, carrying the sender's id.
	data := json.RawMessage(`{"type":"offer","sdp":"v=0"}`)
	if err := h.relaySignal(ben, ref, idAnn, "offer", data); err != nil {
		t.Fatal(err)
	}
	sig := find(drain(ann), "call.signal")
	if sig == nil || sig["from"] != idBen || sig["kind"] != "offer" {
		t.Fatalf("ann did not receive the signal: %v", sig)
	}
	if len(drain(ben)) != 0 {
		t.Fatal("signal must not echo back to its sender")
	}

	// Rejected signals.
	if got := errCode(h.relaySignal(ben, ref, idCy, "offer", data)); got != "peer_not_in_call" {
		t.Fatalf("signal to non-participant: %q", got)
	}
	if got := errCode(h.relaySignal(ben, ref, idAnn, "bogus", data)); got != "bad_kind" {
		t.Fatalf("bad kind: %q", got)
	}
	if got := errCode(h.relaySignal(ben, ref, idAnn, "offer", nil)); got != "bad_signal" {
		t.Fatalf("empty data: %q", got)
	}
	if got := errCode(h.relaySignal(ben, ref, idBen, "offer", data)); got != "bad_peer" {
		t.Fatalf("signal to self: %q", got)
	}
	big := json.RawMessage(`"` + string(make([]byte, maxSignalBytes)) + `"`)
	if got := errCode(h.relaySignal(ben, ref, idAnn, "offer", big)); got != "bad_signal" {
		t.Fatalf("oversized data: %q", got)
	}

	// Media state is stored and broadcast.
	off := false
	if err := h.setMedia(ben, ref, &off, nil); err != nil {
		t.Fatal(err)
	}
	media := find(drain(ann), "call.media")
	if media == nil || media["userId"] != idBen || media["audio"] != false || media["video"] != true {
		t.Fatalf("media broadcast: %v", media)
	}

	// Leaving: call.left, then call.ended when the last person goes.
	h.leaveCall(ann, ref.key())
	if find(drain(ben), "call.left") == nil {
		t.Fatal("ben was not told ann left")
	}
	h.leaveCall(ben, ref.key())
	if find(drain(ann), "call.ended") == nil {
		t.Fatal("call.ended was not broadcast")
	}
	if got := h.CallFor(ref); len(got) != 0 {
		t.Fatalf("call should be gone, still has %d", len(got))
	}
}

func TestCallRequiresSubscriptionAndMembership(t *testing.T) {
	h := NewHub(nil)
	ann, ben := newTestClient(h, idAnn, "Ann"), newTestClient(h, idBen, "Ben")
	ref := ChannelRef{GroupID: idGroup}

	if _, _, err := h.joinCall(ann, ref, true, false); errCode(err) != "not_subscribed" {
		t.Fatalf("join without subscribing: %v", err)
	}

	for _, c := range []*Client{ann, ben} {
		if _, err := h.subscribe(c, ref); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := h.joinCall(ann, ref, true, false); err != nil {
		t.Fatal(err)
	}
	// Ben is subscribed but has not joined, so he cannot signal into the call.
	data := json.RawMessage(`{"sdp":"x"}`)
	if got := errCode(h.relaySignal(ben, ref, idAnn, "offer", data)); got != "not_in_call" {
		t.Fatalf("signal from non-participant: %q", got)
	}
	if got := errCode(h.setMedia(ben, ref, nil, nil)); got != "not_in_call" {
		t.Fatalf("media from non-participant: %q", got)
	}
}

func TestOneConnectionPerUserInCall(t *testing.T) {
	h := NewHub(nil)
	tab1, tab2 := newTestClient(h, idAnn, "Ann"), newTestClient(h, idAnn, "Ann")
	ref := ChannelRef{GroupID: idGroup}
	for _, c := range []*Client{tab1, tab2} {
		if _, err := h.subscribe(c, ref); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := h.joinCall(tab1, ref, true, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.joinCall(tab2, ref, true, false); errCode(err) != "already_in_call" {
		t.Fatalf("second tab: %v", err)
	}
	// Closing the second tab must not remove the first tab from the call.
	h.leaveAllCalls(tab2)
	if got := h.CallFor(ref); len(got) != 1 {
		t.Fatalf("first tab was dropped from the call: %d participants", len(got))
	}
}

func TestCallCapacity(t *testing.T) {
	h := NewHub(nil)
	ref := ChannelRef{GroupID: idGroup}
	for i := 0; i < MaxCallParticipants; i++ {
		c := newTestClient(h, fmt.Sprintf("00000000-0000-0000-0000-%012d", i+1), "u")
		if _, err := h.subscribe(c, ref); err != nil {
			t.Fatal(err)
		}
		if _, _, err := h.joinCall(c, ref, true, false); err != nil {
			t.Fatalf("participant %d: %v", i+1, err)
		}
	}
	extra := newTestClient(h, idCy, "Cy")
	if _, err := h.subscribe(extra, ref); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.joinCall(extra, ref, true, false); errCode(err) != "call_full" {
		t.Fatalf("over capacity: %v", err)
	}
}

func TestUnsubscribeAndDisconnectLeaveTheCall(t *testing.T) {
	h := NewHub(nil)
	ann, ben := newTestClient(h, idAnn, "Ann"), newTestClient(h, idBen, "Ben")
	ref := ChannelRef{GroupID: idGroup}
	for _, c := range []*Client{ann, ben} {
		if _, err := h.subscribe(c, ref); err != nil {
			t.Fatal(err)
		}
		if _, _, err := h.joinCall(c, ref, true, false); err != nil {
			t.Fatal(err)
		}
	}
	drain(ann)
	drain(ben)

	// Losing channel access (the reauth path) removes the person from the call.
	h.unsubscribe(ann, ref.key(), "access_revoked")
	if got := h.CallFor(ref); len(got) != 1 || got[0].UserID != idBen {
		t.Fatalf("after unsubscribe: %+v", got)
	}

	// A dropped connection does too.
	h.unregister(ben)
	if got := h.CallFor(ref); len(got) != 0 {
		t.Fatalf("after disconnect: %+v", got)
	}
}

func TestICEServersUseExpiringHMACCredentials(t *testing.T) {
	cfg := ICEConfig{
		STUN:   []string{"stun:stun.example.com:3478"},
		TURN:   []string{"turn:turn.example.com:3478?transport=udp"},
		Secret: "s3cret",
	}
	now := time.Unix(1_700_000_000, 0)
	servers := cfg.Servers("user-1", now)
	if len(servers) != 2 {
		t.Fatalf("want STUN + TURN entries, got %d", len(servers))
	}

	wantUser := fmt.Sprintf("%d:user-1", now.Add(ICECredentialTTL).Unix())
	mac := hmac.New(sha1.New, []byte("s3cret"))
	mac.Write([]byte(wantUser))
	wantCred := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	turn := servers[1]
	if turn.Username != wantUser || turn.Credential != wantCred {
		t.Fatalf("turn credentials: got %q / %q, want %q / %q",
			turn.Username, turn.Credential, wantUser, wantCred)
	}
	if servers[0].Username != "" || servers[0].Credential != "" {
		t.Fatal("STUN entry must not carry credentials")
	}

	// Without a secret there is no TURN entry, and the secret never appears.
	noTurn := ICEConfig{STUN: cfg.STUN}.Servers("user-1", now)
	if len(noTurn) != 1 {
		t.Fatalf("want only STUN, got %d entries", len(noTurn))
	}
}