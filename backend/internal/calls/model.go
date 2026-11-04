// internal/calls/model.go
package calls

import (
	"errors"
	"strings"
	"time"
)

var (
	ErrNotFound     = errors.New("not found")
	ErrForbidden    = errors.New("forbidden")
	ErrInvalid      = errors.New("invalid request")
	ErrCallFull     = errors.New("call is full")
	ErrNotConnected = errors.New("open the realtime connection and subscribe to the room first")
	ErrNotInCall    = errors.New("you are not in this call")
)

// MaxParticipants caps a call. Calls are a peer-to-peer mesh (every person
// sends their camera to every other person), which is fine for study rooms but
// gets heavy past ~6. Beyond that, move to an SFU (LiveKit, mediasoup, ...).
const MaxParticipants = 6

// Role mirrors groups.Role numerically (kept separate to avoid an import cycle).
type Role int

const (
	RoleNone Role = iota
	RoleMember
	RoleModerator
	RoleAdmin
	RoleOwner
)

func ParseRole(s string) Role {
	switch s {
	case "owner":
		return RoleOwner
	case "admin":
		return RoleAdmin
	case "moderator":
		return RoleModerator
	case "member":
		return RoleMember
	default:
		return RoleNone
	}
}

// Call is one live (or finished) call in a room. At most one is active per room.
type Call struct {
	ID        string
	GroupID   string
	RoomID    string
	StartedBy *string
	StartedAt time.Time
	EndedAt   *time.Time
}

type Participant struct {
	UserID   string
	Name     string
	AudioOn  bool
	VideoOn  bool
	JoinedAt time.Time
}

// CallView is a call plus who is currently in it.
type CallView struct {
	Call
	Participants []Participant
}

type JoinResult struct {
	View       *CallView
	Me         Participant
	ICEServers []ICEServer
}

// Room is the slice of study_rooms the access gate needs.
type Room struct {
	ID       string
	GroupID  string
	Access   string // "group" | "restricted"
	Archived bool
}

// leftRef describes a call a user was moved out of (joined elsewhere).
type leftRef struct {
	CallID  string
	RoomID  string
	GroupID string
	Ended   bool
}

type ICEConfig struct {
	STUNURLs      []string
	TURNURLs      []string
	TURNSecret    string // coturn "static-auth-secret"
	CredentialTTL time.Duration
}

type ICEServer struct {
	URLs       []string
	Username   string
	Credential string
}

// Realtime is what calls needs from the WebSocket hub (realtime.Hub satisfies it).
type Realtime interface {
	Broadcast(topicKey, typ string, data any)
	SetInCall(roomID, userID string, in bool)
	IsPresent(topicKey, userID string) bool
}

func roomTopic(roomID string) string { return "r:" + strings.ToLower(roomID) }