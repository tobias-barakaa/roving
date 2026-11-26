// Package stream is the live video session of a room: who is in it, who may
// publish video, and the WebRTC signalling between them.
//
// It is NOT the persistent room (internal/rooms), a focus session, or the
// WebSocket connection itself (internal/realtime). It is in-memory only and
// exists while at least one connection has joined.
//
// Topology: peer-to-peer, one publisher. The host sends video+audio; every
// viewer sends audio only, to the host. Viewers do not connect to each other.
package stream

// Role is what a connection may do inside one room's stream.
type Role string

const (
	RoleHost   Role = "host"   // publishes video + audio
	RoleViewer Role = "viewer" // audio only (mic), receives the host's video
)

// RoleFor maps a role name to a stream Role. Anything but "host" is a viewer.
func RoleFor(name string) Role {
	if name == "host" {
		return RoleHost
	}
	return RoleViewer
}
