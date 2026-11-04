// internal/calls/service.go
package calls

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type Service struct {
	repo *Repository
	db   *pgxpool.Pool
	rt   Realtime
	ice  ICEConfig
}

func NewService(repo *Repository, db *pgxpool.Pool, rt Realtime, ice ICEConfig) *Service {
	return &Service{repo: repo, db: db, rt: rt, ice: ice}
}

// ResetStale closes calls left open by a previous process. Call once at startup.
func (s *Service) ResetStale(ctx context.Context) error {
	return s.repo.CloseAllOpen(ctx, s.db)
}

// ==================== join ====================

// Join puts the caller in the room's call (starting it if nobody is in one).
// The caller must already hold a realtime subscription to the room: signaling
// travels over the WebSocket, and its disconnect is what removes ghosts.
func (s *Service) Join(ctx context.Context, actorID, groupID, roomID string, req JoinCallRequest) (*JoinResult, error) {
	if !uuidRe.MatchString(groupID) || !uuidRe.MatchString(roomID) {
		return nil, ErrNotFound
	}
	audio, video := true, false
	if req.AudioOn != nil {
		audio = *req.AudioOn
	}
	if req.VideoOn != nil {
		video = *req.VideoOn
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if _, err := s.requireRoom(ctx, tx, groupID, actorID, roomID, true); err != nil {
		return nil, err
	}
	if !s.rt.IsPresent(roomTopic(roomID), actorID) {
		return nil, ErrNotConnected
	}

	call, created, err := s.repo.OpenCall(ctx, tx, groupID, roomID, actorID)
	if err != nil {
		return nil, err
	}

	// One live call per person: leave any other call first. If the join fails
	// below, the whole transaction (including this) rolls back.
	moved, err := s.repo.LeaveOtherCalls(ctx, tx, actorID, call.ID)
	if err != nil {
		return nil, err
	}
	for i := range moved {
		ended, err := s.repo.EndCallIfEmpty(ctx, tx, moved[i].CallID)
		if err != nil {
			return nil, err
		}
		moved[i].Ended = ended
	}

	me, inserted, err := s.repo.UpsertParticipant(ctx, tx, call.ID, actorID, audio, video)
	if err != nil {
		if IsUniqueViolation(err) {
			return nil, fmt.Errorf("%w: concurrent join, retry", ErrInvalid)
		}
		return nil, err
	}
	n, err := s.repo.CountActive(ctx, tx, call.ID)
	if err != nil {
		return nil, err
	}
	if inserted && n > MaxParticipants {
		return nil, ErrCallFull
	}
	parts, err := s.repo.ListParticipants(ctx, tx, call.ID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	// Side effects only after a successful commit.
	for _, l := range moved {
		s.rt.SetInCall(l.RoomID, actorID, false)
		s.broadcast(l.RoomID, "call.participant_left", map[string]string{"callId": l.CallID, "userId": actorID})
		if l.Ended {
			s.broadcast(l.RoomID, "call.ended", map[string]string{"callId": l.CallID})
		}
	}
	s.rt.SetInCall(roomID, actorID, true)
	if created {
		s.broadcast(roomID, "call.started", map[string]string{"callId": call.ID, "startedBy": actorID})
	}
	if inserted {
		s.broadcast(roomID, "call.participant_joined", map[string]any{
			"callId": call.ID, "participant": toParticipantResponse(me)})
	}

	return &JoinResult{
		View:       &CallView{Call: *call, Participants: parts},
		Me:         me,
		ICEServers: s.iceServers(actorID),
	}, nil
}

// ==================== read ====================

// Get returns the room's active call, or nil if there isn't one.
func (s *Service) Get(ctx context.Context, actorID, groupID, roomID string) (*CallView, error) {
	if _, err := s.requireRoom(ctx, s.db, groupID, actorID, roomID, false); err != nil {
		return nil, err
	}
	call, err := s.repo.ActiveCallByRoom(ctx, s.db, roomID, false)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	parts, err := s.repo.ListParticipants(ctx, s.db, call.ID)
	if err != nil {
		return nil, err
	}
	return &CallView{Call: *call, Participants: parts}, nil
}

// ==================== leave ====================

// Leave is deliberately not gated on current room access: someone whose access
// was just revoked must still be able to get out of the call.
func (s *Service) Leave(ctx context.Context, actorID, groupID, roomID string) error {
	if !uuidRe.MatchString(groupID) || !uuidRe.MatchString(roomID) {
		return ErrNotFound
	}
	call, err := s.repo.ActiveCallByRoom(ctx, s.db, roomID, false)
	if errors.Is(err, ErrNotFound) {
		return nil // idempotent
	}
	if err != nil {
		return err
	}
	if call.GroupID != groupID {
		return ErrNotFound
	}
	return s.leave(ctx, roomID, actorID)
}

func (s *Service) leave(ctx context.Context, roomID, userID string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	call, err := s.repo.ActiveCallByRoom(ctx, tx, roomID, true)
	if errors.Is(err, ErrNotFound) {
		s.rt.SetInCall(roomID, userID, false)
		return nil
	}
	if err != nil {
		return err
	}
	left, err := s.repo.LeaveParticipant(ctx, tx, call.ID, userID)
	if err != nil {
		return err
	}
	ended := false
	if left {
		if ended, err = s.repo.EndCallIfEmpty(ctx, tx, call.ID); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	s.rt.SetInCall(roomID, userID, false)
	if left {
		s.broadcast(roomID, "call.participant_left", map[string]string{"callId": call.ID, "userId": userID})
	}
	if ended {
		s.broadcast(roomID, "call.ended", map[string]string{"callId": call.ID})
	}
	return nil
}

// OnMemberGone is the hub's leave hook: it fires when a user has had no
// realtime connection to a topic for the whole grace period (closed tab, lost
// network). If that user was in the room's call, remove them.
func (s *Service) OnMemberGone(topicKey, userID string) {
	roomID, ok := strings.CutPrefix(topicKey, "r:")
	if !ok {
		return // group-wide topic
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.leave(ctx, roomID, userID); err != nil {
		log.Printf("calls: auto-leave room %s user %s: %v", roomID, userID, err)
	}
}

// ==================== media flags ====================

// UpdateMedia records mute/camera changes so late joiners see the right state.
// The actual media is switched client-side; this is the shared indicator.
func (s *Service) UpdateMedia(ctx context.Context, actorID, groupID, roomID string, req UpdateMediaRequest) (*Participant, error) {
	if req.AudioOn == nil && req.VideoOn == nil {
		return nil, fmt.Errorf("%w: nothing to update", ErrInvalid)
	}
	if !uuidRe.MatchString(groupID) || !uuidRe.MatchString(roomID) {
		return nil, ErrNotFound
	}
	call, err := s.repo.ActiveCallByRoom(ctx, s.db, roomID, false)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNotInCall
	}
	if err != nil {
		return nil, err
	}
	if call.GroupID != groupID {
		return nil, ErrNotFound
	}
	p, err := s.repo.SetMedia(ctx, s.db, call.ID, actorID, req.AudioOn, req.VideoOn)
	if err != nil {
		return nil, err
	}
	s.broadcast(roomID, "call.participant_updated", map[string]any{
		"callId": call.ID, "participant": toParticipantResponse(p)})
	return &p, nil
}

// ==================== end (moderation) ====================

// End force-closes the call for everyone. Moderator and above.
func (s *Service) End(ctx context.Context, actorID, groupID, roomID string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	role, err := s.requireRoom(ctx, tx, groupID, actorID, roomID, false)
	if err != nil {
		return err
	}
	if role < RoleModerator {
		return ErrForbidden
	}
	call, err := s.repo.ActiveCallByRoom(ctx, tx, roomID, true)
	if err != nil {
		return err // ErrNotFound -> 404: no active call
	}
	users, err := s.repo.EndCall(ctx, tx, call.ID)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	for _, u := range users {
		s.rt.SetInCall(roomID, u, false)
	}
	s.broadcast(roomID, "call.ended", map[string]string{"callId": call.ID, "endedBy": actorID})
	return nil
}

// ==================== gate & helpers ====================

// requireRoom is the one access gate. Same rules as chat: non-member ->
// NotFound (anti-enumeration), suspended -> Forbidden, hidden restricted room
// -> NotFound, archived rooms are read-only (no new joins).
func (s *Service) requireRoom(ctx context.Context, ex pgxExecutor, groupID, userID, roomID string, write bool) (Role, error) {
	if !uuidRe.MatchString(groupID) || !uuidRe.MatchString(roomID) {
		return RoleNone, ErrNotFound
	}
	roleStr, status, err := s.repo.MemberRole(ctx, ex, groupID, userID)
	if err != nil {
		return RoleNone, err
	}
	if status != "active" {
		return RoleNone, ErrForbidden
	}
	role := ParseRole(roleStr)

	room, err := s.repo.GetRoom(ctx, ex, roomID)
	if err != nil {
		return RoleNone, err
	}
	if room.GroupID != strings.ToLower(groupID) {
		return RoleNone, ErrNotFound
	}
	if room.Access == "restricted" {
		ok, err := s.repo.HasRoomMembership(ctx, ex, room.ID, userID)
		if err != nil {
			return RoleNone, err
		}
		if !ok {
			return RoleNone, ErrNotFound
		}
	}
	if write && room.Archived {
		return RoleNone, fmt.Errorf("%w: this room is archived", ErrInvalid)
	}
	return role, nil
}

func (s *Service) broadcast(roomID, typ string, data any) {
	s.rt.Broadcast(roomTopic(roomID), typ, data)
}

// iceServers returns STUN plus short-lived TURN credentials (coturn's
// "use-auth-secret" REST scheme): username = "<expiry>:<userId>",
// credential = base64(HMAC-SHA1(secret, username)). Nothing long-lived is
// ever sent to the browser.
func (s *Service) iceServers(userID string) []ICEServer {
	var out []ICEServer
	if len(s.ice.STUNURLs) > 0 {
		out = append(out, ICEServer{URLs: s.ice.STUNURLs})
	}
	if len(s.ice.TURNURLs) > 0 && s.ice.TURNSecret != "" {
		ttl := s.ice.CredentialTTL
		if ttl <= 0 {
			ttl = 6 * time.Hour
		}
		user := fmt.Sprintf("%d:%s", time.Now().Add(ttl).Unix(), userID)
		mac := hmac.New(sha1.New, []byte(s.ice.TURNSecret))
		mac.Write([]byte(user))
		out = append(out, ICEServer{
			URLs:       s.ice.TURNURLs,
			Username:   user,
			Credential: base64.StdEncoding.EncodeToString(mac.Sum(nil)),
		})
	}
	return out
}