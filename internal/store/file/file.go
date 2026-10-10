// Package file 是单租户存储：一份 hosts.json，没有账号。
package file

import (
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/corral-dev/corral-relay/internal/store"
)

type persistedHost struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	TokenHash    string `json:"token_hash,omitempty"`
	Ed25519Pub   string `json:"ed25519_pub,omitempty"`
	NodeID       string `json:"node_id,omitempty"`
	BundleID     string `json:"bundle_id,omitempty"`
	Banned       bool   `json:"banned,omitempty"`
	RegisteredAt int64  `json:"registered_at"`
	LastSeenAt   int64  `json:"last_seen_at"`
}

type Store struct {
	mu    sync.RWMutex
	path  string
	hosts map[string]*store.Host
	quota store.Quota
	usage map[string]*counters
	dirty bool
}

type counters struct {
	month     string
	bytes     int64
	pushesDay string
	pushes    int
}

func Open(path string) (*Store, error) {
	s := &Store{
		path:  path,
		hosts: map[string]*store.Host{},
		usage: map[string]*counters{},
		quota: store.Quota{}, // 0 = 不限额
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	var rows []*persistedHost
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, err
	}
	for _, row := range rows {
		h := fromPersisted(row)
		s.hosts[h.ID] = h
	}
	return s, nil
}

func fromPersisted(row *persistedHost) *store.Host {
	h := &store.Host{
		ID:           row.ID,
		Name:         row.Name,
		TokenHash:    row.TokenHash,
		NodeID:       row.NodeID,
		BundleID:     row.BundleID,
		RegisteredAt: row.RegisteredAt,
		LastSeenAt:   row.LastSeenAt,
	}
	if row.Ed25519Pub != "" {
		if raw, err := hex.DecodeString(row.Ed25519Pub); err == nil {
			h.Ed25519Pub = raw
		}
	}
	if row.Banned {
		now := time.Unix(row.LastSeenAt, 0)
		h.BannedAt = &now
	}
	return h
}

func toPersisted(h *store.Host) *persistedHost {
	row := &persistedHost{
		ID:           h.ID,
		Name:         h.Name,
		TokenHash:    h.TokenHash,
		Ed25519Pub:   hex.EncodeToString(h.Ed25519Pub),
		NodeID:       h.NodeID,
		BundleID:     h.BundleID,
		RegisteredAt: h.RegisteredAt,
		LastSeenAt:   h.LastSeenAt,
	}
	if h.Banned() {
		row.Banned = true
	}
	return row
}

func (s *Store) Lookup(id string) (*store.Host, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	host, ok := s.hosts[id]
	return host, ok
}

func (s *Store) AuthenticateV1(id, name, tokenHash string, online bool) (*store.Host, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().Unix()
	host, ok := s.hosts[id]
	if !ok {
		host = &store.Host{ID: id, Name: name, TokenHash: tokenHash, RegisteredAt: now}
		s.hosts[id] = host
		s.dirty = true
	} else if subtle.ConstantTimeCompare([]byte(host.TokenHash), []byte(tokenHash)) != 1 {
		return nil, store.ErrTokenMismatch
	}
	host.LastSeenAt = now
	if name != "" && host.Name != name {
		host.Name = name
		s.dirty = true
	}
	s.dirty = true
	return host, nil
}

func (s *Store) AuthenticateV2(id, name string, pub []byte, online, tofu, replaceWhenOffline bool) (*store.Host, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().Unix()
	host, ok := s.hosts[id]
	if !ok {
		if !tofu {
			return nil, store.ErrHostUnknown
		}
		host = &store.Host{ID: id, Name: name, Ed25519Pub: append([]byte(nil), pub...), RegisteredAt: now, LastSeenAt: now}
		s.hosts[id] = host
		s.dirty = true
		return host, nil
	}
	if host.Banned() {
		return nil, store.ErrHostBanned
	}
	if len(host.Ed25519Pub) > 0 && subtle.ConstantTimeCompare(host.Ed25519Pub, pub) != 1 {
		return nil, store.ErrKeyMismatch
	} else if len(host.Ed25519Pub) == 0 {
		host.Ed25519Pub = append([]byte(nil), pub...)
		s.dirty = true
	}
	host.LastSeenAt = now
	if name != "" && host.Name != name {
		host.Name = name
		s.dirty = true
	}
	return host, nil
}

func (s *Store) RotateEd25519(id string, pub []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	host, ok := s.hosts[id]
	if !ok {
		return store.ErrHostUnknown
	}
	if host.Banned() {
		return store.ErrHostBanned
	}
	if len(pub) != 32 {
		return store.ErrKeyMismatch
	}
	host.Ed25519Pub = append([]byte(nil), pub...)
	host.RegisteredAt = time.Now().Unix()
	s.dirty = true
	return nil
}

func (s *Store) RegisterHost(_, id, name string, pub []byte, bundleID string) (*store.Host, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().Unix()
	host, ok := s.hosts[id]
	if !ok {
		host = &store.Host{ID: id, RegisteredAt: now}
		s.hosts[id] = host
	}
	host.Name = name
	host.Ed25519Pub = append([]byte(nil), pub...)
	host.BundleID = bundleID
	host.LastSeenAt = now
	s.dirty = true
	return host, nil
}

func (s *Store) SetNode(id, nodeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	host, ok := s.hosts[id]
	if !ok {
		return store.ErrHostUnknown
	}
	if host.NodeID != nodeID {
		host.NodeID = nodeID
		s.dirty = true
	}
	return nil
}

func (s *Store) TouchSeen(id, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	host, ok := s.hosts[id]
	if !ok {
		return store.ErrHostUnknown
	}
	host.LastSeenAt = time.Now().Unix()
	if name != "" {
		host.Name = name
	}
	s.dirty = true
	return nil
}

func (s *Store) Ban(id string, banned bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	host, ok := s.hosts[id]
	if !ok {
		return store.ErrHostUnknown
	}
	if banned {
		now := time.Now()
		host.BannedAt = &now
	} else {
		host.BannedAt = nil
	}
	s.dirty = true
	return nil
}

func (s *Store) ListHosts(string) ([]*store.Host, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*store.Host, 0, len(s.hosts))
	for _, h := range s.hosts {
		out = append(out, h)
	}
	return out, nil
}

func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.hosts)
}

func (s *Store) Flush() error {
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return nil
	}
	rows := make([]*persistedHost, 0, len(s.hosts))
	for _, h := range s.hosts {
		rows = append(rows, toPersisted(h))
	}
	s.dirty = false
	s.mu.Unlock()

	data, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) UpsertAccount(string, string, string) (*store.Account, error) {
	return nil, store.ErrNotSupported
}
func (s *Store) CreateCLIToken(string) (string, error) { return "", store.ErrNotSupported }
func (s *Store) AccountByToken(string) (*store.Account, error) {
	return nil, store.ErrNotSupported
}
func (s *Store) CreateDeviceCode(string, string, time.Duration) (*store.DeviceCode, error) {
	return nil, store.ErrNotSupported
}
func (s *Store) LookupDeviceCode(string) (*store.DeviceCode, error) {
	return nil, store.ErrNotSupported
}
func (s *Store) LookupUserCode(string) (*store.DeviceCode, error) {
	return nil, store.ErrNotSupported
}
func (s *Store) ApproveDeviceCode(string, string) error { return store.ErrNotSupported }
func (s *Store) ConsumeDeviceCode(string) (*store.Account, string, error) {
	return nil, "", store.ErrNotSupported
}

func (s *Store) QuotaFor(string) store.Quota {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.quota
}

func (s *Store) SetQuotaDefaults(q store.Quota) {
	s.mu.Lock()
	s.quota = q
	s.mu.Unlock()
}

func (s *Store) AddBytes(accountID string, n int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.counter(accountID)
	c.bytes += n
}

func (s *Store) AddPushes(accountID string, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.counter(accountID)
	today := time.Now().UTC().Format("2006-01-02")
	if c.pushesDay != today {
		c.pushesDay = today
		c.pushes = 0
	}
	c.pushes += n
}

func (s *Store) counter(accountID string) *counters {
	c := s.usage[accountID]
	if c == nil {
		c = &counters{month: time.Now().UTC().Format("2006-01")}
		s.usage[accountID] = c
	}
	return c
}
