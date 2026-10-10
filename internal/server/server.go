// Package server 提供中继对外入口：开发机注册、手机接入、可选的多租户账号面。
package server

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/coder/websocket"

	"github.com/corral-dev/corral-relay/internal/hub"
	"github.com/corral-dev/corral-relay/internal/metrics"
	"github.com/corral-dev/corral-relay/internal/protocol"
	"github.com/corral-dev/corral-relay/internal/store"
	"github.com/corral-dev/corral-relay/internal/web"
)

const (
	maxFrameBytes       = 8 << 20
	writeTimeout        = 15 * time.Second
	pingInterval        = 25 * time.Second
	deviceConnectBudget = 30
	hostConnectBudget   = 20
	deviceConnectWindow = time.Minute
)

type Options struct {
	MultiTenant  bool
	NodeID       string
	PublicURL    string
	GitHubID     string
	GitHubSecret string
	Cookies      *web.CookieJar
}

type Server struct {
	hub    *hub.Hub
	store  store.Store
	log    *slog.Logger
	opts   Options
	nonces *protocol.NonceCache

	mu     sync.Mutex
	budget map[string]*ipBudget
}

type ipBudget struct {
	reset time.Time
	count int
}

func New(h *hub.Hub, s store.Store, log *slog.Logger, opts Options) *Server {
	if opts.Cookies == nil {
		opts.Cookies = web.NewCookieJar(nil)
	}
	if opts.NodeID == "" {
		opts.NodeID = "local"
	}
	h.SetNodeID(opts.NodeID)
	return &Server{
		hub: h, store: s, log: log, opts: opts,
		nonces: protocol.NewNonceCache(),
		budget: map[string]*ipBudget{},
	}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/host", s.handleHostV2)
	mux.HandleFunc("/v2/host/attach", s.handleHostAttachV2)
	mux.HandleFunc("/v2/device", s.handleDeviceV2)
	mux.HandleFunc("/v1/host", s.handleHostV1)
	mux.HandleFunc("/v1/device", s.handleDeviceV1)
	mux.HandleFunc("/healthz", s.handleHealth)
	if s.opts.MultiTenant {
		mux.HandleFunc("/v2/auth/device", s.handleAuthDevice)
		mux.HandleFunc("/v2/auth/token", s.handleAuthToken)
		mux.HandleFunc("/v2/hosts", s.handleRegisterHost)
		mux.HandleFunc("/login", s.handleLogin)
		mux.HandleFunc("/oauth/github/callback", s.handleGitHubCallback)
		mux.HandleFunc("/device", s.handleDevicePage)
		mux.HandleFunc("/account", s.handleAccountPage)
	}
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func rejectBrowserOrigin(w http.ResponseWriter, r *http.Request) bool {
	if strings.TrimSpace(r.Header.Get("Origin")) == "" {
		return false
	}
	http.Error(w, "拒绝浏览器跨站连接", http.StatusForbidden)
	return true
}

func headerFirst(r *http.Request, names ...string) string {
	for _, name := range names {
		if v := strings.TrimSpace(r.Header.Get(name)); v != "" {
			return v
		}
	}
	return ""
}

func sanitizeName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r < 32 || r == 127 || r == '\u2028' || r == '\u2029' {
			continue
		}
		if !unicode.IsPrint(r) {
			continue
		}
		b.WriteRune(r)
		if b.Len() >= 80 {
			break
		}
	}
	return strings.TrimSpace(b.String())
}

func clientIP(r *http.Request) string {
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peer = r.RemoteAddr
	}
	if forwarded := forwardedClientIP(r, peer); forwarded != "" {
		return forwarded
	}
	return peer
}

func forwardedClientIP(r *http.Request, peer string) string {
	ip := net.ParseIP(strings.TrimSpace(peer))
	if ip == nil || !isTrustedProxy(ip) {
		return ""
	}
	if raw := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); raw != "" {
		first := strings.TrimSpace(strings.Split(raw, ",")[0])
		if parsed := net.ParseIP(first); parsed != nil {
			return parsed.String()
		}
	}
	if raw := strings.TrimSpace(r.Header.Get("X-Real-IP")); raw != "" {
		if parsed := net.ParseIP(raw); parsed != nil {
			return parsed.String()
		}
	}
	return ""
}

func isTrustedProxy(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return true
	}
	return false
}

func (s *Server) allowBudget(key string, limit int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	entry := s.budget[key]
	if entry == nil || now.After(entry.reset) {
		s.budget[key] = &ipBudget{reset: now.Add(deviceConnectWindow), count: 1}
		return true
	}
	if entry.count >= limit {
		return false
	}
	entry.count++
	return true
}

func (s *Server) allowDeviceConnect(ip, hostID string) bool {
	if !s.allowBudget("ip:"+ip, deviceConnectBudget) {
		return false
	}
	if hostID != "" && !s.allowBudget("host:"+hostID, hostConnectBudget) {
		return false
	}
	return true
}

func (s *Server) failAuth(w http.ResponseWriter, reason string) {
	metrics.AuthFailures.Add(1)
	s.log.Info("开发机鉴权失败", "reason", reason)
	http.Error(w, "无法注册", http.StatusUnauthorized)
}

func presentedHostPub(r *http.Request) ed25519.PublicKey {
	raw, err := hex.DecodeString(strings.TrimSpace(r.Header.Get("X-Corral-Host-Pub")))
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil
	}
	return raw
}

func (s *Server) verifyHostV2(r *http.Request, assertion protocol.Assertion) (ed25519.PublicKey, error) {
	now := time.Now()
	presented := presentedHostPub(r)
	stored, ok := s.store.Lookup(assertion.RoutingID)

	if ok && len(stored.Ed25519Pub) == ed25519.PublicKeySize {
		if len(presented) == ed25519.PublicKeySize && subtle.ConstantTimeCompare(stored.Ed25519Pub, presented) != 1 {
			if s.opts.MultiTenant {
				return nil, store.ErrKeyMismatch
			}
			prev, err := protocol.ParseAssertion(r.Header.Get("X-Corral-Prev-Auth"))
			if err != nil || !protocol.AssertionsBound(assertion, prev) {
				return nil, store.ErrKeyMismatch
			}
			if err := protocol.CheckAssertion(stored.Ed25519Pub, prev, now); err != nil {
				return nil, err
			}
			if err := protocol.CheckAssertion(presented, assertion, now); err != nil {
				return nil, err
			}
			if !s.nonces.Accept(assertion.RoutingID, assertion.Nonce, now) {
				return nil, protocol.ErrAuthReplay
			}
			if err := s.store.RotateEd25519(assertion.RoutingID, presented); err != nil {
				return nil, err
			}
			return presented, nil
		}
		if err := protocol.VerifyAssertion(stored.Ed25519Pub, assertion, now, s.nonces); err != nil {
			return nil, err
		}
		return stored.Ed25519Pub, nil
	}

	if s.opts.MultiTenant {
		return nil, store.ErrHostUnknown
	}
	if len(presented) != ed25519.PublicKeySize {
		return nil, store.ErrHostUnknown
	}
	if err := protocol.VerifyAssertion(presented, assertion, now, s.nonces); err != nil {
		return nil, err
	}
	return presented, nil
}

func (s *Server) handleHostV2(w http.ResponseWriter, r *http.Request) {
	if rejectBrowserOrigin(w, r) {
		return
	}
	assertion, err := protocol.ParseAssertion(r.Header.Get("X-Corral-Auth"))
	if err != nil {
		s.failAuth(w, "断言格式")
		return
	}
	name := sanitizeName(headerFirst(r, "X-Corral-Host-Name"))
	bundle := strings.TrimSpace(r.Header.Get("X-Corral-Bundle-Id"))
	_, online := s.hub.Online(assertion.RoutingID)

	pub, err := s.verifyHostV2(r, assertion)
	if err != nil {
		if errors.Is(err, store.ErrHostBanned) {
			http.Error(w, "已封禁", http.StatusForbidden)
			return
		}
		s.failAuth(w, err.Error())
		return
	}

	host, err := s.store.AuthenticateV2(assertion.RoutingID, name, pub, online, !s.opts.MultiTenant, false)
	if err != nil {
		if errors.Is(err, store.ErrHostBanned) {
			http.Error(w, "已封禁", http.StatusForbidden)
			return
		}
		s.failAuth(w, err.Error())
		return
	}
	if s.opts.MultiTenant {
		q := s.store.QuotaFor(host.AccountID)
		if q.MaxHosts > 0 && !online && s.hub.AccountOnline(host.AccountID) >= q.MaxHosts {
			http.Error(w, "超出开发机配额", http.StatusTooManyRequests)
			return
		}
		if q.MonthlyBytes > 0 && q.BytesUsed >= q.MonthlyBytes {
			http.Error(w, "超出流量配额", http.StatusTooManyRequests)
			return
		}
	}
	if bundle != "" {
		host.BundleID = bundle
	}
	_ = s.store.SetNode(host.ID, s.opts.NodeID)

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{protocol.Subprotocol}})
	if err != nil {
		return
	}
	conn.SetReadLimit(maxFrameBytes)
	sink := newSink(conn)
	maxPeers := 16
	if s.opts.MultiTenant {
		if q := s.store.QuotaFor(host.AccountID); q.MaxDevicesPerHost > 0 {
			maxPeers = q.MaxDevicesPerHost
		}
	}
	live := s.hub.RegisterHost(host.ID, host.Name, host.AccountID, host.BundleID, protocol.Version, maxPeers, sink)
	s.log.Info("开发机上线", "host", host.ID, "name", name, "v", 2, "generation", live.Generation)
	defer func() {
		s.hub.UnregisterHost(live)
		s.log.Info("开发机下线", "host", host.ID)
	}()

	go keepAlive(conn)
	for {
		kind, frame, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		if kind != websocket.MessageBinary {
			continue
		}
		if err := s.hub.FromHostLane(live, protocol.LaneInteraction, frame); err != nil {
			if errors.Is(err, hub.ErrFrameTooShort) {
				continue
			}
			return
		}
	}
}

func (s *Server) handleHostAttachV2(w http.ResponseWriter, r *http.Request) {
	if rejectBrowserOrigin(w, r) {
		return
	}
	attach, err := protocol.ParseAttachAssertion(r.Header.Get("X-Corral-Lane-Attach"))
	if err != nil {
		s.failAuth(w, "附着断言格式")
		return
	}
	if attach.Lane == protocol.LaneInteraction {
		http.Error(w, "不能附着主通道", http.StatusBadRequest)
		return
	}
	if attach.NodeID == "" || attach.NodeID != s.opts.NodeID {
		http.Error(w, "附着节点不匹配", http.StatusConflict)
		return
	}
	live, online := s.hub.Online(attach.RoutingID)
	if !online {
		http.Error(w, "开发机当前不在线", http.StatusServiceUnavailable)
		return
	}
	if live.Generation != attach.Generation {
		http.Error(w, "注册代次已失效", http.StatusConflict)
		return
	}

	if _, err := s.verifyHostAttach(r, attach); err != nil {
		if errors.Is(err, store.ErrHostBanned) {
			http.Error(w, "已封禁", http.StatusForbidden)
			return
		}
		s.failAuth(w, err.Error())
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{protocol.Subprotocol}})
	if err != nil {
		return
	}
	conn.SetReadLimit(maxFrameBytes)
	sink := newSink(conn)
	_, attachID, err := s.hub.AttachHostLane(attach.RoutingID, attach.Generation, attach.Lane, sink)
	if err != nil {
		_ = conn.Close(websocket.StatusPolicyViolation, err.Error())
		return
	}
	defer s.hub.DetachHostLane(attach.RoutingID, attach.Lane, attach.Generation, attachID)
	s.log.Info("开发机附着通道", "host", attach.RoutingID, "lane", attach.Lane, "generation", attach.Generation)
	go keepAlive(conn)
	for {
		kind, frame, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		if kind != websocket.MessageBinary {
			continue
		}
		if err := s.hub.FromHostLane(live, attach.Lane, frame); err != nil {
			if errors.Is(err, hub.ErrFrameTooShort) {
				continue
			}
			return
		}
	}
}

func (s *Server) verifyHostAttach(r *http.Request, attach protocol.AttachAssertion) (ed25519.PublicKey, error) {
	now := time.Now()
	presented := presentedHostPub(r)
	stored, ok := s.store.Lookup(attach.RoutingID)
	if !ok || len(stored.Ed25519Pub) != ed25519.PublicKeySize {
		return nil, store.ErrHostUnknown
	}
	if len(presented) == ed25519.PublicKeySize && subtle.ConstantTimeCompare(stored.Ed25519Pub, presented) != 1 {
		return nil, store.ErrKeyMismatch
	}
	if err := protocol.VerifyAttachAssertion(stored.Ed25519Pub, attach, now, s.nonces); err != nil {
		return nil, err
	}
	return stored.Ed25519Pub, nil
}

func (s *Server) handleHostV1(w http.ResponseWriter, r *http.Request) {
	if rejectBrowserOrigin(w, r) {
		return
	}
	hostID := headerFirst(r, "X-Corral-Host", "X-Pickup-Host")
	token := strings.TrimPrefix(strings.TrimSpace(r.Header.Get("Authorization")), "Bearer ")
	if hostID == "" || token == "" {
		http.Error(w, "缺少开发机标识或注册凭据", http.StatusUnauthorized)
		return
	}
	name := sanitizeName(headerFirst(r, "X-Corral-Host-Name", "X-Pickup-Host-Name"))
	sum := sha256.Sum256([]byte(token))
	_, online := s.hub.Online(hostID)
	host, err := s.store.AuthenticateV1(hostID, name, hex.EncodeToString(sum[:]), online)
	if err != nil {
		http.Error(w, "无法注册", http.StatusUnauthorized)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{})
	if err != nil {
		return
	}
	conn.SetReadLimit(maxFrameBytes)
	sink := newSink(conn)
	live := s.hub.RegisterHost(host.ID, host.Name, "", "", 1, 16, sink)
	s.log.Info("开发机上线", "host", hostID, "name", name, "v", 1)
	defer func() {
		s.hub.UnregisterHost(live)
		s.log.Info("开发机下线", "host", hostID)
	}()
	go keepAlive(conn)
	for {
		kind, frame, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		if kind != websocket.MessageBinary {
			continue
		}
		if err := s.hub.FromHostLane(live, protocol.LaneInteraction, frame); err != nil {
			if errors.Is(err, hub.ErrFrameTooShort) {
				continue
			}
			return
		}
	}
}

func (s *Server) handleDeviceV2(w http.ResponseWriter, r *http.Request) {
	s.handleDevice(w, r, protocol.Version)
}

func (s *Server) handleDeviceV1(w http.ResponseWriter, r *http.Request) {
	s.handleDevice(w, r, 1)
}

func (s *Server) handleDevice(w http.ResponseWriter, r *http.Request, version byte) {
	if rejectBrowserOrigin(w, r) {
		return
	}
	hostID := strings.TrimSpace(r.URL.Query().Get("host"))
	if hostID == "" {
		http.Error(w, "缺少开发机标识", http.StatusBadRequest)
		return
	}
	if !s.allowDeviceConnect(clientIP(r), hostID) {
		http.Error(w, "连接太频繁", http.StatusTooManyRequests)
		return
	}
	if stored, ok := s.store.Lookup(hostID); ok {
		if stored.Banned() {
			http.Error(w, "开发机已被封禁", http.StatusForbidden)
			return
		}
		if stored.NodeID != "" && stored.NodeID != s.opts.NodeID {
			if _, online := s.hub.Online(hostID); !online {
				w.Header().Set("X-Corral-Node", stored.NodeID)
				http.Error(w, "开发机在另一个节点", http.StatusServiceUnavailable)
				return
			}
		}
		if s.opts.MultiTenant {
			q := s.store.QuotaFor(stored.AccountID)
			if q.MonthlyBytes > 0 && q.BytesUsed >= q.MonthlyBytes {
				http.Error(w, "超出流量配额", http.StatusTooManyRequests)
				return
			}
		}
	}
	host, online := s.hub.Online(hostID)
	if !online {
		http.Error(w, "开发机当前不在线", http.StatusServiceUnavailable)
		return
	}
	lane := strings.TrimSpace(r.URL.Query().Get("lane"))
	if lane == "" {
		lane = protocol.LaneInteraction
	}
	if _, err := protocol.NormalizeLane(lane); err != nil {
		http.Error(w, "通道种类不合法", http.StatusBadRequest)
		return
	}
	opts := &websocket.AcceptOptions{}
	if version == protocol.Version {
		opts.Subprotocols = []string{protocol.Subprotocol}
	}
	conn, err := websocket.Accept(w, r, opts)
	if err != nil {
		return
	}
	conn.SetReadLimit(maxFrameBytes)
	sink := newSink(conn)
	channelID, err := s.hub.AttachDeviceLane(hostID, lane, sink)
	if err != nil {
		conn.Close(websocket.StatusTryAgainLater, "开发机暂时接不了")
		return
	}
	defer s.hub.DetachDevice(host, channelID)

	go keepAlive(conn)
	for {
		kind, frame, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		if kind != websocket.MessageBinary {
			continue
		}
		if err := s.hub.FromDevice(host, channelID, frame); err != nil {
			if errors.Is(err, hub.ErrFrameTooShort) {
				continue
			}
			return
		}
	}
}

func keepAlive(conn *websocket.Conn) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for range ticker.C {
		ctx, cancel := contextWithTimeout(writeTimeout)
		err := conn.Ping(ctx)
		cancel()
		if err != nil {
			return
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
