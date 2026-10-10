package push

import (
	"encoding/json"
	"os"
	"sync"
)

// Router 按 bundle id 选推送 profile。自建单租户只有一份默认配置。
type Router struct {
	mu       sync.RWMutex
	fallback *Sender
	byBundle map[string]*Sender
}

func NewRouter(fallback *Sender) *Router {
	return &Router{fallback: fallback, byBundle: map[string]*Sender{}}
}

func (r *Router) Add(bundle string, sender *Sender) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byBundle[bundle] = sender
}

func (r *Router) Send(token, env, hostID, bundleID string, payload []byte) (Result, error) {
	return r.SendFor(bundleID, token, env, hostID, payload)
}

func (r *Router) SendFor(bundle, token, env, hostID string, payload []byte) (Result, error) {
	r.mu.RLock()
	sender := r.byBundle[bundle]
	if sender == nil {
		sender = r.fallback
	}
	r.mu.RUnlock()
	if sender == nil {
		// Missing push configuration is not success: the caller must answer
		// with a no_push_config receipt.
		return Result{}, ErrPushNotConfigured
	}
	return sender.Send(token, env, hostID, payload)
}

type fileProfile struct {
	KeyPath  string `json:"key_path"`
	KeyID    string `json:"key_id"`
	TeamID   string `json:"team_id"`
	BundleID string `json:"bundle_id"`
}

// LoadProfilesFile 读可选的多档推送 JSON：[{"bundle_id":"...","key_path":"...","key_id":"...","team_id":"..."}]
func LoadProfilesFile(path string) ([]Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rows []fileProfile
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, err
	}
	out := make([]Config, 0, len(rows))
	for _, row := range rows {
		out = append(out, Config{KeyPath: row.KeyPath, KeyID: row.KeyID, TeamID: row.TeamID, BundleID: row.BundleID})
	}
	return out, nil
}
