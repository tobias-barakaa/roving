package stream

import (
	"encoding/json"
	"sync"

	"roving/backend/internal/realtime"
)

// Registry holds every active stream, keyed by room id.
type Registry struct {
	mu      sync.RWMutex
	streams map[string]*Stream
	byConn  map[string]string // connID -> roomID (one stream per connection)
}

func NewRegistry() *Registry {
	return &Registry{streams: map[string]*Stream{}, byConn: map[string]string{}}
}

func (r *Registry) get(roomID string) *Stream {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.streams[roomID]
}

// join puts a connection into a room's stream, first removing it from any
// stream it was already in. It returns the stream, the effective role, and the
// stream the connection left (nil when none, or when it is the same room).
func (r *Registry) join(groupID, roomID string, p *Participant) (s *Stream, role Role, left *Stream) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if prevRoom, ok := r.byConn[p.ConnID]; ok {
		if prev := r.streams[prevRoom]; prev != nil {
			prev.remove(p.ConnID)
			if prevRoom != roomID {
				left = prev
			}
			if prev.isEmpty() {
				delete(r.streams, prevRoom)
				if left == prev {
					left = nil // nobody left to tell
				}
			}
		}
		delete(r.byConn, p.ConnID)
	}

	s = r.streams[roomID]
	if s == nil {
		s = newStream(groupID, roomID)
		r.streams[roomID] = s
	}
	role = s.add(p)
	r.byConn[p.ConnID] = roomID
	return s, role, left
}

// leave removes a connection from whatever stream it is in. It returns that
// stream (nil if none) and whether anyone is left in it.
func (r *Registry) leave(connID string) (*Stream, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	roomID, ok := r.byConn[connID]
	if !ok {
		return nil, false
	}
	delete(r.byConn, connID)
	s := r.streams[roomID]
	if s == nil {
		return nil, false
	}
	s.remove(connID)
	if s.isEmpty() {
		delete(r.streams, roomID)
		return s, false
	}
	return s, true
}

// broadcastState sends the current roster to every participant.
func (r *Registry) broadcastState(s *Stream, hub *realtime.Hub) {
	roster, hostConn := s.snapshot()
	b, _ := json.Marshal(map[string]any{
		"roomId":       s.RoomID,
		"hostConnId":   hostConn,
		"hostCamera":   s.hostCamera(),
		"participants": roster,
	})
	env := realtime.Envelope{Type: TypeStreamState, Payload: b}
	for _, p := range roster {
		hub.SendToConnection(p.ConnID, env)
	}
}

// RoomConnections lists the connections in a room. realtime.RegisterChat uses it.
func (r *Registry) RoomConnections(roomID string) []string {
	s := r.get(roomID)
	if s == nil {
		return nil
	}
	roster, _ := s.snapshot()
	out := make([]string, 0, len(roster))
	for _, p := range roster {
		out = append(out, p.ConnID)
	}
	return out
}
