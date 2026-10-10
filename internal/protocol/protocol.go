// Package protocol 是中继层 v2 的共享约定：帧头、路由标识派生、开发机签名断言。
//
// 字符串一律用 corral，不引入旧产品名。
package protocol

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	Version     byte = 2
	Subprotocol      = "corral.v2"

	FrameDeviceOpen   = 0x01
	FrameDeviceClose  = 0x02
	FrameHello        = 0x03
	FramePing         = 0x04
	FramePong         = 0x05
	FramePush         = 0x06
	FrameRegistered   = 0x07
	FrameLaneAttached = 0x08
	FramePushReceipt  = 0x09
	FrameData         = 0x10

	ChannelIDLen  = 16
	MaxFrameBytes = 8 << 20
	NonceSize     = 16
	AuthSkew      = 60 * time.Second
	NonceTTL      = 120 * time.Second
	V1HeaderLen   = 1 + ChannelIDLen
	V2HeaderLen   = 2 + ChannelIDLen

	LaneInteraction = "interaction"
	LaneBulk        = "bulk"

	// CapabilityHostLaneAttach is advertised on FrameRegistered so old hosts
	// keep a single socket while new hosts may attach a bulk lane.
	CapabilityHostLaneAttach = "host_lane_attach"
)

var (
	RouteContext  = []byte("corral/relay/v2 route")
	AuthContext   = []byte("corral/relay/v2 host-auth")
	AttachContext = []byte("corral/relay/v2 host-lane-attach")

	ErrFrameTooShort = errors.New("帧过短")
	ErrFrameTooLarge = errors.New("帧过大")
	ErrBadVersion    = errors.New("帧版本不是 v2")
	ErrBadAuth       = errors.New("开发机断言不合法")
	ErrAuthSkew      = errors.New("开发机时钟偏差过大")
	ErrAuthReplay    = errors.New("开发机断言重放")
	ErrBadSignature  = errors.New("开发机签名不匹配")
	ErrBadLane       = errors.New("通道种类不合法")
)

type Assertion struct {
	RoutingID string
	UnixTS    int64
	Nonce     []byte
	Sig       []byte
}

// AttachAssertion binds an extra host socket to an existing registration
// generation. It cannot create or replace the primary registration.
type AttachAssertion struct {
	RoutingID  string
	NodeID     string
	Generation uint64
	Lane       string
	UnixTS     int64
	Nonce      []byte
	Sig        []byte
}

func RoutingID(x25519Pub []byte) string {
	sum := sha256.Sum256(append(append([]byte{}, RouteContext...), x25519Pub...))
	enc := base32.StdEncoding.EncodeToString(sum[:16])
	return strings.ToLower(strings.TrimRight(enc, "="))
}

func EncodeV2(kind byte, id [ChannelIDLen]byte, payload []byte) []byte {
	frame := make([]byte, V2HeaderLen+len(payload))
	frame[0] = Version
	frame[1] = kind
	copy(frame[2:], id[:])
	copy(frame[V2HeaderLen:], payload)
	return frame
}

func EncodeV1(kind byte, id [ChannelIDLen]byte, payload []byte) []byte {
	frame := make([]byte, V1HeaderLen+len(payload))
	frame[0] = kind
	copy(frame[1:], id[:])
	copy(frame[V1HeaderLen:], payload)
	return frame
}

func DecodeV2(raw []byte) (kind byte, id [ChannelIDLen]byte, payload []byte, err error) {
	if len(raw) < V2HeaderLen {
		return 0, id, nil, ErrFrameTooShort
	}
	if len(raw) > MaxFrameBytes {
		return 0, id, nil, ErrFrameTooLarge
	}
	if raw[0] != Version {
		return 0, id, nil, ErrBadVersion
	}
	kind = raw[1]
	copy(id[:], raw[2:2+ChannelIDLen])
	payload = raw[V2HeaderLen:]
	return kind, id, payload, nil
}

func DecodeV1(raw []byte) (kind byte, id [ChannelIDLen]byte, payload []byte, err error) {
	if len(raw) < V1HeaderLen {
		return 0, id, nil, ErrFrameTooShort
	}
	if len(raw) > MaxFrameBytes {
		return 0, id, nil, ErrFrameTooLarge
	}
	kind = raw[0]
	copy(id[:], raw[1:1+ChannelIDLen])
	payload = raw[V1HeaderLen:]
	return kind, id, payload, nil
}

func SignMessage(routingID string, unixTS int64, nonce []byte) []byte {
	var b strings.Builder
	b.Grow(len(AuthContext) + len(routingID) + 32 + len(nonce))
	b.Write(AuthContext)
	b.WriteByte('\n')
	b.WriteString(routingID)
	b.WriteByte('\n')
	b.WriteString(strconv.FormatInt(unixTS, 10))
	b.WriteByte('\n')
	msg := append([]byte(b.String()), nonce...)
	return msg
}

func SignAttachMessage(routingID, nodeID string, generation uint64, lane string, unixTS int64, nonce []byte) []byte {
	var b strings.Builder
	b.Grow(len(AttachContext) + len(routingID) + len(nodeID) + len(lane) + 48 + len(nonce))
	b.Write(AttachContext)
	b.WriteByte('\n')
	b.WriteString(routingID)
	b.WriteByte('\n')
	b.WriteString(nodeID)
	b.WriteByte('\n')
	b.WriteString(strconv.FormatUint(generation, 10))
	b.WriteByte('\n')
	b.WriteString(lane)
	b.WriteByte('\n')
	b.WriteString(strconv.FormatInt(unixTS, 10))
	b.WriteByte('\n')
	msg := append([]byte(b.String()), nonce...)
	return msg
}

func NormalizeLane(lane string) (string, error) {
	switch strings.TrimSpace(strings.ToLower(lane)) {
	case "", LaneInteraction:
		return LaneInteraction, nil
	case LaneBulk:
		return LaneBulk, nil
	default:
		return "", ErrBadLane
	}
}

func FormatAssertion(routingID string, unixTS int64, nonce, sig []byte) string {
	return fmt.Sprintf("v2.%s.%d.%s.%s", routingID, unixTS, b64(nonce), b64(sig))
}

func FormatAttachAssertion(routingID, nodeID string, generation uint64, lane string, unixTS int64, nonce, sig []byte) string {
	return fmt.Sprintf(
		"v2attach.%s.%s.%d.%s.%d.%s.%s",
		routingID, nodeID, generation, lane, unixTS, b64(nonce), b64(sig),
	)
}

func ParseAssertion(header string) (Assertion, error) {
	var out Assertion
	parts := strings.Split(strings.TrimSpace(header), ".")
	if len(parts) != 5 || parts[0] != "v2" {
		return out, ErrBadAuth
	}
	ts, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return out, ErrBadAuth
	}
	nonce, err := unb64(parts[3])
	if err != nil || len(nonce) != NonceSize {
		return out, ErrBadAuth
	}
	sig, err := unb64(parts[4])
	if err != nil || len(sig) != ed25519.SignatureSize {
		return out, ErrBadAuth
	}
	out.RoutingID = parts[1]
	out.UnixTS = ts
	out.Nonce = nonce
	out.Sig = sig
	return out, nil
}

func CheckAssertion(pub ed25519.PublicKey, a Assertion, now time.Time) error {
	if pub == nil || len(pub) != ed25519.PublicKeySize {
		return ErrBadSignature
	}
	skew := now.Sub(time.Unix(a.UnixTS, 0))
	if skew < 0 {
		skew = -skew
	}
	if skew > AuthSkew {
		return ErrAuthSkew
	}
	msg := SignMessage(a.RoutingID, a.UnixTS, a.Nonce)
	if !ed25519.Verify(pub, msg, a.Sig) {
		return ErrBadSignature
	}
	return nil
}

func AssertionsBound(a, prev Assertion) bool {
	if a.RoutingID != prev.RoutingID || a.UnixTS != prev.UnixTS {
		return false
	}
	return subtle.ConstantTimeCompare(a.Nonce, prev.Nonce) == 1
}

func VerifyAssertion(pub ed25519.PublicKey, a Assertion, now time.Time, seen *NonceCache) error {
	if err := CheckAssertion(pub, a, now); err != nil {
		return err
	}
	if seen != nil && !seen.Accept(a.RoutingID, a.Nonce, now) {
		return ErrAuthReplay
	}
	return nil
}

func ParseAttachAssertion(header string) (AttachAssertion, error) {
	var out AttachAssertion
	parts := strings.Split(strings.TrimSpace(header), ".")
	if len(parts) != 8 || parts[0] != "v2attach" {
		return out, ErrBadAuth
	}
	generation, err := strconv.ParseUint(parts[3], 10, 64)
	if err != nil {
		return out, ErrBadAuth
	}
	lane, err := NormalizeLane(parts[4])
	if err != nil {
		return out, err
	}
	ts, err := strconv.ParseInt(parts[5], 10, 64)
	if err != nil {
		return out, ErrBadAuth
	}
	nonce, err := unb64(parts[6])
	if err != nil || len(nonce) != NonceSize {
		return out, ErrBadAuth
	}
	sig, err := unb64(parts[7])
	if err != nil || len(sig) != ed25519.SignatureSize {
		return out, ErrBadAuth
	}
	out.RoutingID = parts[1]
	out.NodeID = parts[2]
	out.Generation = generation
	out.Lane = lane
	out.UnixTS = ts
	out.Nonce = nonce
	out.Sig = sig
	return out, nil
}

func CheckAttachAssertion(pub ed25519.PublicKey, a AttachAssertion, now time.Time) error {
	if pub == nil || len(pub) != ed25519.PublicKeySize {
		return ErrBadSignature
	}
	if _, err := NormalizeLane(a.Lane); err != nil {
		return err
	}
	skew := now.Sub(time.Unix(a.UnixTS, 0))
	if skew < 0 {
		skew = -skew
	}
	if skew > AuthSkew {
		return ErrAuthSkew
	}
	msg := SignAttachMessage(a.RoutingID, a.NodeID, a.Generation, a.Lane, a.UnixTS, a.Nonce)
	if !ed25519.Verify(pub, msg, a.Sig) {
		return ErrBadSignature
	}
	return nil
}

func VerifyAttachAssertion(pub ed25519.PublicKey, a AttachAssertion, now time.Time, seen *NonceCache) error {
	if err := CheckAttachAssertion(pub, a, now); err != nil {
		return err
	}
	if seen != nil && !seen.Accept("attach:"+a.RoutingID, a.Nonce, now) {
		return ErrAuthReplay
	}
	return nil
}

func b64(raw []byte) string {
	return strings.TrimRight(base64.RawURLEncoding.EncodeToString(raw), "=")
}

func unb64(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}

// NonceCache 记住最近用过的 (routing_id, nonce)，挡住重放。
type NonceCache struct {
	mu      sync.Mutex
	entries map[string]time.Time
}

func NewNonceCache() *NonceCache {
	return &NonceCache{entries: map[string]time.Time{}}
}

func (c *NonceCache) Accept(routingID string, nonce []byte, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := routingID + ":" + string(nonce)
	if exp, ok := c.entries[key]; ok && now.Before(exp) {
		return false
	}
	c.entries[key] = now.Add(NonceTTL)
	if len(c.entries) > 4096 {
		for k, exp := range c.entries {
			if now.After(exp) {
				delete(c.entries, k)
			}
		}
	}
	return true
}
