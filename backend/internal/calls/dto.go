// internal/calls/dto.go
package calls

import "time"

// Both fields optional. Defaults on join: audio on, video off.
type JoinCallRequest struct {
	AudioOn *bool `json:"audioOn"`
	VideoOn *bool `json:"videoOn"`
}

type UpdateMediaRequest struct {
	AudioOn *bool `json:"audioOn"`
	VideoOn *bool `json:"videoOn"`
}

type ParticipantResponse struct {
	UserID   string    `json:"userId"`
	Name     string    `json:"name"`
	AudioOn  bool      `json:"audioOn"`
	VideoOn  bool      `json:"videoOn"`
	JoinedAt time.Time `json:"joinedAt"`
}

type CallResponse struct {
	ID              string                `json:"id"`
	GroupID         string                `json:"groupId"`
	RoomID          string                `json:"roomId"`
	StartedBy       *string               `json:"startedBy,omitempty"`
	StartedAt       time.Time             `json:"startedAt"`
	MaxParticipants int                   `json:"maxParticipants"`
	Participants    []ParticipantResponse `json:"participants"`
}

type ICEServerResponse struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

// Feed ICEServers straight into new RTCPeerConnection({ iceServers }).
type JoinResponse struct {
	Call       CallResponse        `json:"call"`
	Me         ParticipantResponse `json:"me"`
	ICEServers []ICEServerResponse `json:"iceServers"`
}

func toParticipantResponse(p Participant) ParticipantResponse {
	return ParticipantResponse{UserID: p.UserID, Name: p.Name, AudioOn: p.AudioOn, VideoOn: p.VideoOn, JoinedAt: p.JoinedAt}
}

func toCallResponse(v *CallView) CallResponse {
	ps := make([]ParticipantResponse, len(v.Participants))
	for i, p := range v.Participants {
		ps[i] = toParticipantResponse(p)
	}
	return CallResponse{
		ID: v.ID, GroupID: v.GroupID, RoomID: v.RoomID, StartedBy: v.StartedBy,
		StartedAt: v.StartedAt, MaxParticipants: MaxParticipants, Participants: ps,
	}
}

func toJoinResponse(r *JoinResult) JoinResponse {
	ice := make([]ICEServerResponse, len(r.ICEServers))
	for i, s := range r.ICEServers {
		ice[i] = ICEServerResponse{URLs: s.URLs, Username: s.Username, Credential: s.Credential}
	}
	return JoinResponse{Call: toCallResponse(r.View), Me: toParticipantResponse(r.Me), ICEServers: ice}
}