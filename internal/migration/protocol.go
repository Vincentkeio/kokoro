// Package migration contains the small authenticated handoff protocol shared
// by Hub and Agent. No endpoint accepts shell commands or new node credentials.
package migration

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
)

const Capability = 1

type Proof struct {
	Nonce string `json:"nonce"`
	MAC   string `json:"mac"`
}
type Arrival struct {
	NodeID   string `json:"node_id"`
	LastSeen int64  `json:"last_seen"`
}
type Response struct {
	BackupID   string    `json:"backup_id"`
	MAC        string    `json:"mac"`
	Arrivals   []Arrival `json:"arrivals,omitempty"`
	RestoredAt int64     `json:"restored_at"`
}
type NodeResponse struct {
	NodeID   string `json:"node_id"`
	BackupID string `json:"backup_id"`
}

func MAC(key, scope, nonce string) string {
	h := hmac.New(sha256.New, []byte(key))
	h.Write([]byte(scope + "\n" + nonce))
	return hex.EncodeToString(h.Sum(nil))
}
func Verify(key, scope, nonce, mac string) bool {
	return len(nonce) == 64 && hmac.Equal([]byte(MAC(key, scope, nonce)), []byte(mac))
}
func Target(raw string) (string, error) {
	u, e := url.Parse(strings.TrimSpace(raw))
	if e != nil || u == nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.Opaque != "" {
		return "", errors.New("请输入完整的 HTTPS 面板地址，例如 https://panel.example.com")
	}
	u.Path = ""
	u.RawPath = ""
	return strings.TrimRight(u.String(), "/"), nil
}
