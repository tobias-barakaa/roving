package stream

import (
	"sort"
	"sync"
	"time"
)

// Participant is one connected tab taking part in a room's stream.
// Keyed by ConnID because a user can have several tabs open.
type Participant struct {
	ConnID   string    `json:"connectionId"`
	UserID   string    `json:"userId"`
	Role     Role      `json:"role"` // EFFECTIVE role: "host" only for the one publisher
	JoinedAt time.Time `json:"joinedAt"`
}

// Stream is the in-memory state of one room's live session.
type Stream struct {
	mu           sync.RWMutex
	RoomID       string
	GroupID      string
	HostConnID   string // the one connection allowed to publish video ("" = nobody yet)
	HostCamera   bool   // the host says their camera is on (a plain flag so viewers can show "camera off" at once)
	Participants map[string]*Participant
}

func newStream(groupID, roomID string) *Stream {
	return &Stream{RoomID: roomID, GroupID: groupID, Participants: map[string]*Participant{}}
}

// add inserts a participant and returns the role they actually got.
// There is exactly one publisher. If a second host-capable tab joins (the owner
// opened the room twice), it joins as a viewer instead of fighting for the slot.
func (s *Stream) add(p *Participant) Role {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.Role == RoleHost {
		if s.HostConnID == "" {
			s.HostConnID = p.ConnID
		} else {
			p.Role = RoleViewer
		}
	}
	s.Participants[p.ConnID] = p
	return p.Role
}

func (s *Stream) remove(connID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.Participants, connID)
	if s.HostConnID == connID {
		s.HostConnID = ""
		s.HostCamera = false
	}
}

// setHostCamera records the host's camera state. Only the publisher's own
// connection may change it. It reports whether anything changed.
func (s *Stream) setHostCamera(connID string, on bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.HostConnID != connID || s.HostCamera == on {
		return false
	}
	s.HostCamera = on
	return true
}

func (s *Stream) isHostConn(connID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.HostConnID == connID
}

func (s *Stream) has(connID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.Participants[connID]
	return ok
}

func (s *Stream) isEmpty() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.Participants) == 0
}

// snapshot returns the roster, oldest first, plus the current host connection.
func (s *Stream) hostCamera() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.HostCamera
}

func (s *Stream) snapshot() ([]Participant, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Participant, 0, len(s.Participants))
	for _, p := range s.Participants {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].JoinedAt.Before(out[j].JoinedAt) })
	return out, s.HostConnID
}
