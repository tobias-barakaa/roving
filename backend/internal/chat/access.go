package chat
 
import "context"
 
// CanAccessChannel reports whether the user may read a chat channel
// (roomID nil = the group-wide channel). It applies exactly the same rules as
// the HTTP endpoints, so the real-time layer can never be a side door:
// non-members and hidden restricted rooms get ErrNotFound, inactive members
// get ErrForbidden.
func (s *Service) CanAccessChannel(ctx context.Context, userID, groupID string, roomID *string) error {
	_, err := s.requireChannel(ctx, s.db, groupID, userID, roomID, false)
	return err
}
 
// PublicResponse renders a message for broadcast to many viewers, so IsMine is
// always false; clients compare userId with their own.
func PublicResponse(m *Message) MessageResponse {
	return toResponse(m, "")
}