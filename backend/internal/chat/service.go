// internal/chat/service.go
package chat

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type Service struct {
	repo *Repository
	db   *pgxpool.Pool
	pub  Publisher // optional; attached later by the real-time layer
}

func NewService(repo *Repository, db *pgxpool.Pool) *Service {
	return &Service{repo: repo, db: db}
}

// SetPublisher attaches the real-time fan-out. Call it once at startup,
// before serving requests.
func (s *Service) SetPublisher(p Publisher) { s.pub = p }

func (s *Service) publish(typ string, m *Message) {
	if s.pub == nil || m == nil {
		return
	}
	s.pub.Publish(Event{Type: typ, GroupID: m.GroupID, RoomID: m.RoomID, Message: m})
}

// ==================== send ====================

// Send posts to the group-wide channel (roomID nil) or to a room.
func (s *Service) Send(ctx context.Context, actorID, groupID string, roomID *string, req SendMessageRequest) (*Message, error) {
	body := strings.TrimSpace(req.Body)
	if body == "" {
		return nil, fmt.Errorf("%w: message is empty", ErrInvalid)
	}
	if _, err := s.requireChannel(ctx, s.db, groupID, actorID, roomID, true); err != nil {
		return nil, err
	}

	if req.ReplyToID != nil {
		parent, err := s.repo.GetMessage(ctx, s.db, *req.ReplyToID)
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("%w: reply target not found", ErrInvalid)
		}
		if err != nil {
			return nil, err
		}
		if parent.GroupID != groupID || !sameRoom(parent.RoomID, roomID) {
			return nil, fmt.Errorf("%w: reply target is in another channel", ErrInvalid)
		}
		if parent.DeletedAt != nil {
			return nil, fmt.Errorf("%w: cannot reply to a deleted message", ErrInvalid)
		}
	}

	uid := actorID
	m, err := s.repo.Insert(ctx, s.db, &Message{
		GroupID:   groupID,
		RoomID:    roomID,
		UserID:    &uid,
		ReplyToID: req.ReplyToID,
		Body:      body,
	})
	if err != nil {
		return nil, mapDBErr(err)
	}
	s.publish(EventMessageCreated, m)
	return m, nil
}

// ==================== history ====================

// List returns one page in chronological order. hasMore means more messages
// exist beyond this page in the direction you were paging (older for
// default/before, newer for after).
func (s *Service) List(ctx context.Context, actorID, groupID string, roomID *string,
	before, after string, limit int) ([]Message, bool, error) {

	if before != "" && after != "" {
		return nil, false, fmt.Errorf("%w: use either before or after, not both", ErrInvalid)
	}
	for _, id := range []string{before, after} {
		if id != "" && !uuidRe.MatchString(id) {
			return nil, false, fmt.Errorf("%w: malformed cursor", ErrInvalid)
		}
	}
	if _, err := s.requireChannel(ctx, s.db, groupID, actorID, roomID, false); err != nil {
		return nil, false, err
	}

	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	rows, err := s.repo.ListMessages(ctx, s.db, groupID, roomID, before, after, limit+1)
	if err != nil {
		return nil, false, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	if after == "" { // fetched newest-first; hand back oldest-first
		for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
			rows[i], rows[j] = rows[j], rows[i]
		}
	}
	return rows, hasMore, nil
}

// ==================== edit / delete ====================

// Edit lets an author change their own live message.
func (s *Service) Edit(ctx context.Context, actorID, groupID, messageID string, req EditMessageRequest) (*Message, error) {
	body := strings.TrimSpace(req.Body)
	if body == "" {
		return nil, fmt.Errorf("%w: message is empty", ErrInvalid)
	}
	cur, err := s.loadMessage(ctx, groupID, messageID)
	if err != nil {
		return nil, err
	}
	if _, err := s.requireChannel(ctx, s.db, groupID, actorID, cur.RoomID, true); err != nil {
		return nil, err
	}
	if cur.UserID == nil || *cur.UserID != actorID {
		return nil, ErrForbidden
	}
	m, err := s.repo.EditMessage(ctx, s.db, messageID, actorID, body)
	if err != nil {
		return nil, mapDBErr(err) // ErrNotFound if it was deleted meanwhile
	}
	s.publish(EventMessageUpdated, m)
	return m, nil
}

// Delete soft-deletes. Authors can delete their own; moderators and above can
// delete anyone's in channels they can access.
func (s *Service) Delete(ctx context.Context, actorID, groupID, messageID string) error {
	cur, err := s.loadMessage(ctx, groupID, messageID)
	if err != nil {
		return err
	}
	role, err := s.requireChannel(ctx, s.db, groupID, actorID, cur.RoomID, false)
	if err != nil {
		return err
	}
	isAuthor := cur.UserID != nil && *cur.UserID == actorID
	if !isAuthor && role < RoleModerator {
		return ErrForbidden
	}
	m, err := s.repo.SoftDelete(ctx, s.db, messageID, actorID)
	if err != nil {
		return err
	}
	s.publish(EventMessageDeleted, m)
	return nil
}

// ==================== read state ====================

func (s *Service) MarkRead(ctx context.Context, actorID, groupID string, roomID *string, req MarkReadRequest) error {
	if _, err := s.requireChannel(ctx, s.db, groupID, actorID, roomID, false); err != nil {
		return err
	}
	return s.repo.MarkRead(ctx, s.db, actorID, groupID, roomID, req.MessageID)
}

// Unread summarises unread counts for the group-wide channel and each room
// the caller may enter.
func (s *Service) Unread(ctx context.Context, actorID, groupID string) (int, []RoomUnread, error) {
	if _, err := s.requireGroupRole(ctx, s.db, groupID, actorID); err != nil {
		return 0, nil, err
	}
	general, err := s.repo.GeneralUnread(ctx, s.db, groupID, actorID)
	if err != nil {
		return 0, nil, err
	}
	rooms, err := s.repo.RoomsUnread(ctx, s.db, groupID, actorID)
	if err != nil {
		return 0, nil, err
	}
	return general, rooms, nil
}

// ==================== gates & helpers ====================

// requireChannel is the one access gate for every chat operation.
//   - Must be an active group member (non-member → NotFound, anti-enumeration).
//   - Room channels: the room must be in this group; "restricted" rooms also
//     need an active room membership. Hidden rooms answer NotFound.
//   - Writing to an archived room is refused; reading its history is fine.
func (s *Service) requireChannel(ctx context.Context, ex pgxExecutor, groupID, userID string,
	roomID *string, write bool) (Role, error) {

	role, err := s.requireGroupRole(ctx, ex, groupID, userID)
	if err != nil {
		return RoleNone, err
	}
	if roomID == nil {
		return role, nil
	}
	if !uuidRe.MatchString(*roomID) {
		return RoleNone, ErrNotFound
	}
	room, err := s.repo.GetRoom(ctx, ex, *roomID)
	if err != nil {
		return RoleNone, err
	}
	if room.GroupID != groupID {
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

// requireGroupRole mirrors the other modules' gate: non-member → NotFound,
// non-active → Forbidden. Any active member may chat.
func (s *Service) requireGroupRole(ctx context.Context, ex pgxExecutor, groupID, userID string) (Role, error) {
	if !uuidRe.MatchString(groupID) {
		return RoleNone, ErrNotFound
	}
	var roleStr, status string
	err := ex.QueryRow(ctx,
		`SELECT role::text, status
		   FROM group_memberships
		  WHERE group_id = $1::uuid AND user_id = $2::uuid
		  FOR SHARE`,
		groupID, userID).Scan(&roleStr, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return RoleNone, ErrNotFound
	}
	if err != nil {
		return RoleNone, err
	}
	if status != "active" {
		return RoleNone, ErrForbidden
	}
	return ParseRole(roleStr), nil
}

// loadMessage returns the message only if it belongs to groupID.
func (s *Service) loadMessage(ctx context.Context, groupID, messageID string) (*Message, error) {
	if !uuidRe.MatchString(messageID) {
		return nil, ErrNotFound
	}
	m, err := s.repo.GetMessage(ctx, s.db, messageID)
	if err != nil {
		return nil, err
	}
	if m.GroupID != groupID {
		return nil, ErrNotFound
	}
	return m, nil
}

func sameRoom(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return strings.EqualFold(*a, *b)
}

func mapDBErr(err error) error {
	if IsCheckViolation(err) || IsForeignKeyViolation(err) {
		return fmt.Errorf("%w: value rejected by database constraints", ErrInvalid)
	}
	return err
}