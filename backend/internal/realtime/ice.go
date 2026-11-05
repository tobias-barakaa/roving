// internal/realtime/ice.go
package realtime

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

// ICECredentialTTL is how long TURN credentials stay valid. A call that
// outlives it keeps working; the browser only needs fresh credentials to start
// new connections, so clients should fetch them when joining a call.
const ICECredentialTTL = time.Hour

// ICEServer matches the browser's RTCIceServer shape.
type ICEServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

// ICEConfig describes the STUN/TURN servers handed to browsers.
//
// TURN uses coturn's "REST API" scheme (use-auth-secret): no per-user accounts
// are stored anywhere. The username is "<expiry-unix>:<userId>" and the
// password is base64(HMAC-SHA1(sharedSecret, username)), so credentials expire
// on their own and the secret never leaves the server.
type ICEConfig struct {
	STUN   []string
	TURN   []string
	Secret string
}

// LoadICEConfig reads:
//
//	STUN_URLS    comma-separated; unset → Google's public STUN; set empty → none
//	TURN_URLS    comma-separated, e.g. turn:turn.zelisline.com:3478?transport=udp,turns:turn.zelisline.com:5349?transport=tcp
//	TURN_SECRET  must equal coturn's static-auth-secret
func LoadICEConfig() ICEConfig {
	cfg := ICEConfig{
		TURN:   splitList(os.Getenv("TURN_URLS")),
		Secret: os.Getenv("TURN_SECRET"),
	}
	if v, ok := os.LookupEnv("STUN_URLS"); ok {
		cfg.STUN = splitList(v)
	} else {
		cfg.STUN = []string{"stun:stun.l.google.com:19302"}
	}
	if len(cfg.TURN) == 0 || cfg.Secret == "" {
		cfg.TURN, cfg.Secret = nil, ""
		log.Println("realtime: TURN_URLS / TURN_SECRET not set; calls will fail for users behind strict NATs or firewalls")
	}
	return cfg
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Servers builds the iceServers list for one user at a given time.
func (c ICEConfig) Servers(userID string, now time.Time) []ICEServer {
	out := []ICEServer{}
	if len(c.STUN) > 0 {
		out = append(out, ICEServer{URLs: c.STUN})
	}
	if len(c.TURN) > 0 && c.Secret != "" {
		username := strconv.FormatInt(now.Add(ICECredentialTTL).Unix(), 10) + ":" + userID
		mac := hmac.New(sha1.New, []byte(c.Secret))
		mac.Write([]byte(username))
		out = append(out, ICEServer{
			URLs:       c.TURN,
			Username:   username,
			Credential: base64.StdEncoding.EncodeToString(mac.Sum(nil)),
		})
	}
	return out
}