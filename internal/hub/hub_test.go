package hub

import (
	"sync"
	"testing"
	"time"

	"github.com/corral-dev/corral-relay/internal/protocol"
)

type fakeSink struct {
	mu     sync.Mutex
	frames [][]byte
	closed bool
	block  chan struct{}
	gate   chan struct{}
}

func (f *fakeSink) Send(frame []byte) error {
	if f.block != nil {
		<-f.block
	}
	if f.gate != nil {
		<-f.gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.frames = append(f.frames, append([]byte(nil), frame...))
	return nil
}

func (f *fakeSink) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
}

func (f *fakeSink) kinds() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]byte, 0, len(f.frames))
	for _, frame := range f.frames {
		if len(frame) >= 2 && frame[0] == protocol.Version {
			out = append(out, frame[1])
			continue
		}
		if len(frame) > 0 {
			out = append(out, frame[0])
		}
	}
	return out
}

func (f *fakeSink) last() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.frames) == 0 {
		return nil
	}
	return f.frames[len(f.frames)-1]
}

func (f *fakeSink) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.frames)
}

func waitFrames(t *testing.T, s *fakeSink, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.count() >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d frames, have %d", n, s.count())
}

func TestDeviceFramesReachHostUnchanged(t *testing.T) {
	h := New(nil)
	hostSink := &fakeSink{}
	host := h.RegisterHost("host-1", "开发机", "", "", protocol.Version, 0, hostSink)
	waitFrames(t, hostSink, 1) // REGISTERED

	deviceSink := &fakeSink{}
	id, err := h.AttachDevice("host-1", deviceSink)
	if err != nil {
		t.Fatalf("挂载设备失败: %v", err)
	}
	waitFrames(t, hostSink, 2) // DEVICE_OPEN
	waitFrames(t, deviceSink, 1)

	payload := []byte("这是一段密文，中继不该动它")
	if err := h.FromDevice(host, id, protocol.EncodeV2(protocol.FrameData, id, payload)); err != nil {
		t.Fatalf("转发失败: %v", err)
	}
	waitFrames(t, hostSink, 3)
	got := hostSink.last()
	kind, _, gotPayload, err := protocol.DecodeV2(got)
	if err != nil {
		t.Fatal(err)
	}
	if kind != protocol.FrameData {
		t.Fatalf("帧类型变了: %#x", kind)
	}
	if string(gotPayload) != string(payload) {
		t.Fatalf("载荷被改动了: %q", gotPayload)
	}
}

func TestAttachingToOfflineHostFails(t *testing.T) {
	h := New(nil)
	if _, err := h.AttachDevice("nope", &fakeSink{}); err != ErrHostOffline {
		t.Fatalf("期待开发机不在线，得到 %v", err)
	}
}

func TestReRegisteringHostEvictsOldConnection(t *testing.T) {
	h := New(nil)
	first := &fakeSink{}
	h.RegisterHost("host-1", "开发机", "", "", protocol.Version, 0, first)
	waitFrames(t, first, 1)
	h.RegisterHost("host-1", "开发机", "", "", protocol.Version, 0, &fakeSink{})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		first.mu.Lock()
		closed := first.closed
		first.mu.Unlock()
		if closed {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	first.mu.Lock()
	closed := first.closed
	first.mu.Unlock()
	if !closed {
		t.Fatal("旧连接应当被踢掉，否则手机会连到一条写不出去的线上")
	}
	if h.HostCount() != 1 {
		t.Fatalf("重复注册不该留下两条记录，当前 %d", h.HostCount())
	}
}

func TestDetachNotifiesHost(t *testing.T) {
	h := New(nil)
	hostSink := &fakeSink{}
	host := h.RegisterHost("host-1", "开发机", "", "", protocol.Version, 0, hostSink)
	waitFrames(t, hostSink, 1)
	deviceSink := &fakeSink{}
	id, _ := h.AttachDevice("host-1", deviceSink)
	waitFrames(t, hostSink, 2)
	h.DetachDevice(host, id)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		deviceSink.mu.Lock()
		closed := deviceSink.closed
		deviceSink.mu.Unlock()
		if closed {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	deviceSink.mu.Lock()
	closed := deviceSink.closed
	deviceSink.mu.Unlock()
	if !closed {
		t.Fatal("设备连接应当被关闭")
	}
	waitFrames(t, hostSink, 3)
	kinds := hostSink.kinds()
	if kinds[len(kinds)-1] != protocol.FrameDeviceClose {
		t.Fatalf("开发机应当收到设备断开通知，实际 %#x", kinds)
	}
}

func TestDeviceLimitPerHost(t *testing.T) {
	h := New(nil)
	h.RegisterHost("host-1", "开发机", "", "", protocol.Version, maxDevicesPerHost, &fakeSink{})
	for i := 0; i < maxDevicesPerHost; i++ {
		if _, err := h.AttachDevice("host-1", &fakeSink{}); err != nil {
			t.Fatalf("第 %d 台设备就失败了: %v", i, err)
		}
	}
	if _, err := h.AttachDevice("host-1", &fakeSink{}); err != ErrTooManyPeers {
		t.Fatalf("超出上限应当被拒绝，得到 %v", err)
	}
}

func TestAttachHostLaneDoesNotEvictPrimary(t *testing.T) {
	h := New(nil)
	h.SetNodeID("node-a")
	primary := &fakeSink{}
	host := h.RegisterHost("host-1", "开发机", "", "", protocol.Version, 0, primary)
	waitFrames(t, primary, 1)
	bulk := &fakeSink{}
	if _, _, err := h.AttachHostLane("host-1", host.Generation, protocol.LaneBulk, bulk); err != nil {
		t.Fatalf("附着 bulk 失败: %v", err)
	}
	waitFrames(t, bulk, 1)
	primary.mu.Lock()
	closed := primary.closed
	primary.mu.Unlock()
	if closed {
		t.Fatal("附着第二条通道不该关掉主注册连接")
	}
	if h.HostCount() != 1 {
		t.Fatalf("附着不该新增开发机登记，当前 %d", h.HostCount())
	}
	kind, _, payload, err := protocol.DecodeV2(bulk.last())
	if err != nil {
		t.Fatal(err)
	}
	if kind != protocol.FrameLaneAttached {
		t.Fatalf("附着应答类型不对: %#x", kind)
	}
	if len(payload) == 0 || string(payload) == "" {
		t.Fatal("附着应答应带 lane/generation")
	}
}

func TestAttachHostLaneRejectsStaleGeneration(t *testing.T) {
	h := New(nil)
	host := h.RegisterHost("host-1", "开发机", "", "", protocol.Version, 0, &fakeSink{})
	if _, _, err := h.AttachHostLane("host-1", host.Generation+1, protocol.LaneBulk, &fakeSink{}); err != ErrBadGeneration {
		t.Fatalf("过期代次应当被拒，得到 %v", err)
	}
}

func TestAttachHostLaneRejectsInteractionLane(t *testing.T) {
	h := New(nil)
	host := h.RegisterHost("host-1", "开发机", "", "", protocol.Version, 0, &fakeSink{})
	if _, _, err := h.AttachHostLane("host-1", host.Generation, protocol.LaneInteraction, &fakeSink{}); err != ErrAttachPrimary {
		t.Fatalf("interaction 不得作为附着通道，得到 %v", err)
	}
}

func TestFromHostDoesNotBlockReaderOnSlowDevice(t *testing.T) {
	h := New(nil)
	hostSink := &fakeSink{}
	host := h.RegisterHost("host-1", "开发机", "", "", protocol.Version, 0, hostSink)
	waitFrames(t, hostSink, 1)

	slow := &fakeSink{block: make(chan struct{})}
	fast := &fakeSink{}
	slowID, err := h.AttachDevice("host-1", slow)
	if err != nil {
		t.Fatal(err)
	}
	fastID, err := h.AttachDevice("host-1", fast)
	if err != nil {
		t.Fatal(err)
	}
	waitFrames(t, hostSink, 3)

	started := time.Now()
	if err := h.FromHost(host, protocol.EncodeV2(protocol.FrameData, slowID, []byte("slow"))); err != nil {
		t.Fatalf("慢设备入队失败: %v", err)
	}
	if err := h.FromHost(host, protocol.EncodeV2(protocol.FrameData, fastID, []byte("fast"))); err != nil {
		t.Fatalf("快设备入队失败: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
		t.Fatalf("FromHost 不该等慢设备写完，耗时 %s", elapsed)
	}
	waitFrames(t, fast, 2) // DEVICE_OPEN + DATA
	close(slow.block)
	waitFrames(t, slow, 2)
}

func TestBulkDeviceFramesPreferAttachedLane(t *testing.T) {
	h := New(nil)
	h.SetNodeID("node-a")
	primary := &fakeSink{}
	host := h.RegisterHost("host-1", "开发机", "", "", protocol.Version, 0, primary)
	waitFrames(t, primary, 1)
	bulk := &fakeSink{}
	if _, _, err := h.AttachHostLane("host-1", host.Generation, protocol.LaneBulk, bulk); err != nil {
		t.Fatal(err)
	}
	waitFrames(t, bulk, 1)

	device := &fakeSink{}
	id, err := h.AttachDeviceLane("host-1", protocol.LaneBulk, device)
	if err != nil {
		t.Fatal(err)
	}
	waitFrames(t, bulk, 2) // DEVICE_OPEN on bulk lane
	if err := h.FromDevice(host, id, protocol.EncodeV2(protocol.FrameData, id, []byte("bulk-up"))); err != nil {
		t.Fatal(err)
	}
	waitFrames(t, bulk, 3)
	kind, _, payload, err := protocol.DecodeV2(bulk.last())
	if err != nil {
		t.Fatal(err)
	}
	if kind != protocol.FrameData || string(payload) != "bulk-up" {
		t.Fatalf("bulk 设备帧应到 bulk 附着通道, kind=%#x payload=%q", kind, payload)
	}
	before := primary.count()
	time.Sleep(20 * time.Millisecond)
	if primary.count() != before {
		t.Fatal("bulk 上行不该再写到主注册 socket")
	}
}

func TestLegacyLargeFrameMustNotDropHost(t *testing.T) {
	h := New(nil)
	hostSink := &fakeSink{}
	host := h.RegisterHost("host-1", "开发机", "", "", protocol.Version, 0, hostSink)
	waitFrames(t, hostSink, 1)

	deviceSink := &fakeSink{}
	id, err := h.AttachDevice("host-1", deviceSink)
	if err != nil {
		t.Fatal(err)
	}
	waitFrames(t, hostSink, 2)

	payload := make([]byte, 300<<10)
	for i := range payload {
		payload[i] = byte(i)
	}
	frame := protocol.EncodeV2(protocol.FrameData, id, payload)
	if err := h.FromDevice(host, id, frame); err != nil {
		t.Fatalf("合法大帧转发失败: %v", err)
	}
	waitFrames(t, hostSink, 3)

	hostSink.mu.Lock()
	closed := hostSink.closed
	hostSink.mu.Unlock()
	if closed {
		t.Fatal("单帧合法大载荷不得关闭开发机主连接")
	}
	got := hostSink.last()
	kind, _, gotPayload, err := protocol.DecodeV2(got)
	if err != nil {
		t.Fatal(err)
	}
	if kind != protocol.FrameData || len(gotPayload) != len(payload) {
		t.Fatalf("大帧未完整送达: kind=%#x len=%d", kind, len(gotPayload))
	}
}

func TestOldBulkDetachMustNotCloseReplacement(t *testing.T) {
	h := New(nil)
	host := h.RegisterHost("host-1", "开发机", "", "", protocol.Version, 0, &fakeSink{})
	bulkA := &fakeSink{}
	_, attachA, err := h.AttachHostLane("host-1", host.Generation, protocol.LaneBulk, bulkA)
	if err != nil {
		t.Fatal(err)
	}
	waitFrames(t, bulkA, 1)

	bulkB := &fakeSink{}
	_, attachB, err := h.AttachHostLane("host-1", host.Generation, protocol.LaneBulk, bulkB)
	if err != nil {
		t.Fatal(err)
	}
	waitFrames(t, bulkB, 1)

	bulkA.mu.Lock()
	aClosed := bulkA.closed
	bulkA.mu.Unlock()
	if !aClosed {
		t.Fatal("替换附着应当关掉旧 bulk 连接")
	}

	h.DetachHostLane("host-1", protocol.LaneBulk, host.Generation, attachA)

	bulkB.mu.Lock()
	bClosed := bulkB.closed
	bulkB.mu.Unlock()
	if bClosed {
		t.Fatal("旧 bulk 的延迟清理不得关掉替换附着")
	}
	if attachA == attachB {
		t.Fatal("每次附着应分配不同的 attachID")
	}

	h.DetachHostLane("host-1", protocol.LaneBulk, host.Generation, attachB)
	bulkB.mu.Lock()
	bClosed = bulkB.closed
	bulkB.mu.Unlock()
	if !bClosed {
		t.Fatal("匹配 attachID 的 Detach 应关掉当前附着")
	}
}
