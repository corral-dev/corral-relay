// Package hub 负责把开发机与手机接起来，并在两者之间原样转发密文帧。
package hub

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/corral-dev/corral-relay/internal/metrics"
	"github.com/corral-dev/corral-relay/internal/protocol"
	"github.com/corral-dev/corral-relay/internal/push"
)

var (
	ErrHostOffline      = errors.New("开发机当前不在线")
	ErrFrameTooShort    = protocol.ErrFrameTooShort
	ErrTooManyPeers     = errors.New("这台开发机连接的设备过多")
	ErrQuota            = errors.New("超出配额")
	ErrBadGeneration    = errors.New("注册代次已失效")
	ErrAttachPrimary    = errors.New("不能把主连接当附着通道")
	errDestinationReset = errors.New("目的通道已重置")
)

const ChannelIDLen = protocol.ChannelIDLen

const maxDevicesPerHost = 16

const (
	// Queue ceilings must admit a single legal protocol frame (MaxFrameBytes).
	// Soft scheduling budgets may be lower elsewhere; never close the primary
	// host sink solely because one legal frame exceeds a sub-max soft limit.
	queueBytesInteraction = protocol.MaxFrameBytes
	queueBytesBulk        = protocol.MaxFrameBytes
)

type Sink interface {
	Send(frame []byte) error
	Close()
}

type PushSender interface {
	Send(token, env, hostID, bundleID string, payload []byte) (push.Result, error)
}

type ByteRecorder func(accountID string, n int64)

type device struct {
	id   [ChannelIDLen]byte
	lane string
	sink *queuedSink
}

type hostLane struct {
	kind     string
	sink     *queuedSink
	attachID uint64
}

type Host struct {
	ID         string
	Name       string
	AccountID  string
	BundleID   string
	Version    byte
	MaxPeers   int
	NodeID     string
	Generation uint64
	Since      time.Time

	mu        sync.Mutex
	primary   *hostLane
	lanes     map[string]*hostLane
	devices   map[[ChannelIDLen]byte]*device
	scheduler *fairDeviceScheduler
	closed    bool
}

type Hub struct {
	mu      sync.RWMutex
	hosts   map[string]*Host
	push    PushSender
	onBytes ByteRecorder
	gen     atomic.Uint64
	nodeID  string
}

func New(push PushSender) *Hub {
	return &Hub{hosts: map[string]*Host{}, push: push, nodeID: "local"}
}

func (h *Hub) SetByteRecorder(fn ByteRecorder) { h.onBytes = fn }

func (h *Hub) SetNodeID(nodeID string) {
	if nodeID == "" {
		nodeID = "local"
	}
	h.nodeID = nodeID
}

func (h *Hub) NodeID() string { return h.nodeID }

func (h *Hub) RegisterHost(id, name, accountID, bundleID string, version byte, maxPeers int, sink Sink) *Host {
	h.mu.Lock()
	replaced := false
	if previous, ok := h.hosts[id]; ok {
		replaced = true
		h.mu.Unlock()
		previous.shutdown()
		h.mu.Lock()
	}
	if version == 0 {
		version = protocol.Version
	}
	if maxPeers <= 0 {
		maxPeers = maxDevicesPerHost
	}
	generation := h.gen.Add(1)
	primary := &hostLane{
		kind: protocol.LaneInteraction,
		sink: newQueuedSink(sink, queueBytesInteraction),
	}
	host := &Host{
		ID: id, Name: name, AccountID: accountID, BundleID: bundleID,
		Version: version, MaxPeers: maxPeers, NodeID: h.nodeID,
		Generation: generation, Since: time.Now(),
		primary: primary,
		lanes: map[string]*hostLane{
			protocol.LaneInteraction: primary,
		},
		devices:   map[[ChannelIDLen]byte]*device{},
		scheduler: newFairDeviceScheduler(),
	}
	h.hosts[id] = host
	h.mu.Unlock()
	if !replaced {
		metrics.HostsOnline.Add(1)
	}
	_ = primary.sink.Send(h.encode(host, protocol.FrameRegistered, [ChannelIDLen]byte{}, registeredPayload(host)))
	return host
}

// AttachHostLane binds an authenticated extra socket to an existing
// registration generation. It never replaces the primary registration.
// The returned attachID must be passed to DetachHostLane so a deferred
// cleanup from an older socket cannot close its replacement.
func (h *Hub) AttachHostLane(id string, generation uint64, lane string, sink Sink) (*Host, uint64, error) {
	lane, err := protocol.NormalizeLane(lane)
	if err != nil {
		return nil, 0, err
	}
	if lane == protocol.LaneInteraction {
		sink.Close()
		return nil, 0, ErrAttachPrimary
	}
	host, ok := h.Online(id)
	if !ok {
		sink.Close()
		return nil, 0, ErrHostOffline
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.closed || host.Generation != generation {
		sink.Close()
		return nil, 0, ErrBadGeneration
	}
	budget := queueBytesBulk
	if lane == protocol.LaneInteraction {
		budget = queueBytesInteraction
	}
	wrapped := newQueuedSink(sink, budget)
	if previous := host.lanes[lane]; previous != nil && previous != host.primary {
		previous.sink.Close()
	}
	attachID := h.gen.Add(1)
	attached := &hostLane{kind: lane, sink: wrapped, attachID: attachID}
	host.lanes[lane] = attached
	_ = wrapped.Send(h.encode(host, protocol.FrameLaneAttached, [ChannelIDLen]byte{}, laneAttachedPayload(lane, host.Generation)))
	return host, attachID, nil
}

// DetachHostLane drops an extra lane without touching the primary registration.
// attachID must match the current lane owner; stale deferred cleanups are no-ops.
func (h *Hub) DetachHostLane(id, lane string, generation, attachID uint64) {
	lane, err := protocol.NormalizeLane(lane)
	if err != nil || lane == protocol.LaneInteraction {
		return
	}
	host, ok := h.Online(id)
	if !ok {
		return
	}
	host.mu.Lock()
	if host.closed || host.Generation != generation {
		host.mu.Unlock()
		return
	}
	cur := host.lanes[lane]
	if cur == nil || cur == host.primary || cur.attachID != attachID {
		host.mu.Unlock()
		return
	}
	delete(host.lanes, lane)
	host.mu.Unlock()
	cur.sink.Close()
}

func (h *Hub) UnregisterHost(host *Host) {
	h.mu.Lock()
	if current, ok := h.hosts[host.ID]; ok && current == host {
		delete(h.hosts, host.ID)
		metrics.HostsOnline.Add(-1)
	}
	h.mu.Unlock()
	host.shutdown()
}

func (h *Hub) Online(id string) (*Host, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	host, ok := h.hosts[id]
	return host, ok
}

func (h *Hub) HostCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.hosts)
}

func (h *Hub) AccountOnline(accountID string) int {
	if accountID == "" {
		return 0
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	n := 0
	for _, host := range h.hosts {
		if host.AccountID == accountID {
			n++
		}
	}
	return n
}

func (h *Hub) AttachDevice(hostID string, sink Sink) ([ChannelIDLen]byte, error) {
	return h.AttachDeviceLane(hostID, protocol.LaneInteraction, sink)
}

func (h *Hub) AttachDeviceLane(hostID, lane string, sink Sink) ([ChannelIDLen]byte, error) {
	var id [ChannelIDLen]byte
	lane, err := protocol.NormalizeLane(lane)
	if err != nil {
		return id, err
	}
	host, ok := h.Online(hostID)
	if !ok {
		return id, ErrHostOffline
	}
	if _, err := rand.Read(id[:]); err != nil {
		return id, err
	}
	wrapped := newQueuedSink(sink, queueBudgetForLane(lane))
	host.mu.Lock()
	if host.closed {
		host.mu.Unlock()
		wrapped.Close()
		return id, ErrHostOffline
	}
	if len(host.devices) >= host.MaxPeers {
		host.mu.Unlock()
		wrapped.Close()
		return id, ErrTooManyPeers
	}
	host.devices[id] = &device{id: id, lane: lane, sink: wrapped}
	host.scheduler.note(id)
	hostLaneSink := host.laneSinkLocked(lane)
	host.mu.Unlock()
	metrics.Channels.Add(1)

	open := h.encode(host, protocol.FrameDeviceOpen, id, nil)
	if err := hostLaneSink.Send(open); err != nil {
		h.DetachDevice(host, id)
		return id, err
	}
	_ = wrapped.Send(open)
	return id, nil
}

func (h *Hub) DetachDevice(host *Host, id [ChannelIDLen]byte) {
	host.mu.Lock()
	dev, ok := host.devices[id]
	delete(host.devices, id)
	if ok {
		host.scheduler.remove(id)
	}
	notify := host.primary
	if ok {
		if lane := host.lanes[dev.lane]; lane != nil {
			notify = lane
		}
	}
	host.mu.Unlock()
	if !ok {
		return
	}
	metrics.Channels.Add(-1)
	dev.sink.Close()
	if notify != nil {
		_ = notify.sink.Send(h.encode(host, protocol.FrameDeviceClose, id, nil))
	}
}

func (h *Hub) FromDevice(host *Host, id [ChannelIDLen]byte, frame []byte) error {
	kind, _, payload, err := h.decode(host, frame)
	if err != nil {
		if errors.Is(err, protocol.ErrFrameTooShort) {
			return ErrFrameTooShort
		}
		return err
	}
	if kind != protocol.FrameHello && kind != protocol.FrameData {
		return nil
	}
	host.mu.Lock()
	dev, ok := host.devices[id]
	if !ok || host.closed {
		host.mu.Unlock()
		return nil
	}
	laneSink := host.laneSinkLocked(dev.lane)
	host.mu.Unlock()
	h.record(host, len(frame))
	if err := laneSink.Send(h.encode(host, kind, id, payload)); err != nil {
		if errors.Is(err, errDestinationReset) {
			// Only the host lane socket is reset; keep other phones.
			return nil
		}
		return err
	}
	return nil
}

func (h *Hub) FromHost(host *Host, frame []byte) error {
	return h.FromHostLane(host, protocol.LaneInteraction, frame)
}

func (h *Hub) FromHostLane(host *Host, lane string, frame []byte) error {
	lane, err := protocol.NormalizeLane(lane)
	if err != nil {
		return err
	}
	kind, id, payload, err := h.decode(host, frame)
	if err != nil {
		if errors.Is(err, protocol.ErrFrameTooShort) {
			return ErrFrameTooShort
		}
		return err
	}
	if kind == protocol.FramePush {
		if lane != protocol.LaneInteraction {
			return nil
		}
		// Push outcomes never kill the host socket: forwardPush converts them
		// into receipts and always returns nil.
		h.forwardPush(host, payload)
		return nil
	}
	host.mu.Lock()
	dev, ok := host.devices[id]
	host.mu.Unlock()
	if !ok {
		return nil
	}
	if kind == protocol.FrameDeviceClose {
		h.DetachDevice(host, id)
		return nil
	}
	h.record(host, len(frame))
	host.scheduler.note(id)
	if err := dev.sink.Send(h.encode(host, kind, id, payload)); err != nil {
		if errors.Is(err, errDestinationReset) {
			h.DetachDevice(host, id)
			return nil
		}
		return err
	}
	return nil
}

func (host *Host) laneSinkLocked(lane string) *queuedSink {
	if lane == "" {
		lane = protocol.LaneInteraction
	}
	if attached := host.lanes[lane]; attached != nil {
		return attached.sink
	}
	// Degraded: bulk device traffic falls back to the primary socket when
	// the host has not attached that lane yet.
	return host.primary.sink
}

func queueBudgetForLane(lane string) int {
	if lane == protocol.LaneBulk {
		return queueBytesBulk
	}
	return queueBytesInteraction
}

func (h *Hub) record(host *Host, n int) {
	metrics.BytesForwarded.Add(uint64(n))
	if h.onBytes != nil && host.AccountID != "" {
		h.onBytes(host.AccountID, int64(n))
	}
}

func (h *Hub) forwardPush(host *Host, payload []byte) {
	request, err := decodePushRequest(payload)
	if err != nil {
		// Undecodable payloads carry no id to answer: drop them, but never
		// close the host connection over it.
		return
	}
	if h.push == nil {
		h.sendPushReceipt(host, receiptFor(request.ID, push.Result{}, push.ErrPushNotConfigured))
		return
	}
	result, err := h.push.Send(request.Token, request.Env, host.ID, host.BundleID, []byte(request.Payload))
	h.sendPushReceipt(host, receiptFor(request.ID, result, err))
}

// sendPushReceipt answers the host on the interaction lane, best-effort:
// a full queue or a gone connection only drops this receipt, never the host.
func (h *Hub) sendPushReceipt(host *Host, receipt pushReceipt) {
	frame := h.encode(host, protocol.FramePushReceipt, [ChannelIDLen]byte{}, encodePushReceipt(receipt))
	host.mu.Lock()
	sink := host.primary
	host.mu.Unlock()
	if sink == nil {
		return
	}
	_ = sink.sink.Send(frame)
}

func (h *Hub) encode(host *Host, kind byte, id [ChannelIDLen]byte, payload []byte) []byte {
	if host.Version == 1 {
		return protocol.EncodeV1(kind, id, payload)
	}
	return protocol.EncodeV2(kind, id, payload)
}

func (h *Hub) decode(host *Host, raw []byte) (byte, [ChannelIDLen]byte, []byte, error) {
	if host.Version == 1 {
		return protocol.DecodeV1(raw)
	}
	return protocol.DecodeV2(raw)
}

func (host *Host) shutdown() {
	host.mu.Lock()
	if host.closed {
		host.mu.Unlock()
		return
	}
	host.closed = true
	devices := make([]*device, 0, len(host.devices))
	for _, dev := range host.devices {
		devices = append(devices, dev)
	}
	n := len(host.devices)
	host.devices = map[[ChannelIDLen]byte]*device{}
	lanes := make([]*hostLane, 0, len(host.lanes))
	for _, lane := range host.lanes {
		lanes = append(lanes, lane)
	}
	host.lanes = map[string]*hostLane{}
	host.mu.Unlock()
	metrics.Channels.Add(-int64(n))
	for _, dev := range devices {
		dev.sink.Close()
	}
	for _, lane := range lanes {
		lane.sink.Close()
	}
}

func ChannelHex(id [ChannelIDLen]byte) string { return hex.EncodeToString(id[:]) }
