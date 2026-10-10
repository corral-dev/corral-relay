package hub

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/corral-dev/corral-relay/internal/protocol"
	"github.com/corral-dev/corral-relay/internal/push"
)

type fakePushSender struct {
	result push.Result
	err    error
	calls  int
}

func (f *fakePushSender) Send(token, env, hostID, bundleID string, payload []byte) (push.Result, error) {
	f.calls++
	return f.result, f.err
}

func (f *fakeSink) snapshot() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]byte, len(f.frames))
	copy(out, f.frames)
	return out
}

func lastReceipt(t *testing.T, s *fakeSink) pushReceipt {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, frame := range s.snapshot() {
			kind, _, payload, err := protocol.DecodeV2(frame)
			if err != nil || kind != protocol.FramePushReceipt {
				continue
			}
			var receipt pushReceipt
			if err := json.Unmarshal(payload, &receipt); err != nil {
				t.Fatalf("receipt is not JSON: %v", err)
			}
			return receipt
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for PUSH_RECEIPT")
	return pushReceipt{}
}

func pushFrame(t *testing.T, id string) []byte {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"token":   "devicetoken",
		"env":     "production",
		"payload": base64.StdEncoding.EncodeToString([]byte("opaque")),
		"id":      id,
	})
	var zero [ChannelIDLen]byte
	return protocol.EncodeV2(protocol.FramePush, zero, body)
}

func TestPushWithoutConfigReturnsNoPushConfigReceipt(t *testing.T) {
	h := New(nil)
	hostSink := &fakeSink{}
	host := h.RegisterHost("host-1", "dev", "", "", protocol.Version, 0, hostSink)
	waitFrames(t, hostSink, 1)

	h.FromHost(host, pushFrame(t, "p1"))
	receipt := lastReceipt(t, hostSink)
	if receipt.ID != "p1" || receipt.Ok || receipt.Code != "no_push_config" {
		t.Fatalf("missing config must answer no_push_config failure: %+v", receipt)
	}
	hostSink.mu.Lock()
	closed := hostSink.closed
	hostSink.mu.Unlock()
	if closed {
		t.Fatal("push failure must not close the host connection")
	}
}

func TestPushAcceptedReceiptEchoesID(t *testing.T) {
	fake := &fakePushSender{result: push.Result{StatusCode: 200, ApnsID: "apns-9"}}
	h := New(fake)
	hostSink := &fakeSink{}
	host := h.RegisterHost("host-1", "dev", "", "", protocol.Version, 0, hostSink)
	waitFrames(t, hostSink, 1)

	h.FromHost(host, pushFrame(t, "p2"))
	receipt := lastReceipt(t, hostSink)
	if !receipt.Ok || receipt.Code != "ok" || receipt.ID != "p2" || receipt.ApnsID != "apns-9" {
		t.Fatalf("accept must answer ok echoing id/apns_id: %+v", receipt)
	}
}

func TestPushBadTokenKeepsSocketOpen(t *testing.T) {
	fake := &fakePushSender{
		result: push.Result{StatusCode: 410, Reason: "Unregistered"},
		err:    push.ErrBadDeviceToken,
	}
	h := New(fake)
	hostSink := &fakeSink{}
	host := h.RegisterHost("host-1", "dev", "", "", protocol.Version, 0, hostSink)
	waitFrames(t, hostSink, 1)

	h.FromHost(host, pushFrame(t, "p3"))
	receipt := lastReceipt(t, hostSink)
	if receipt.Ok || receipt.Code != "bad_token" || receipt.Reason != "Unregistered" || receipt.Status != 410 {
		t.Fatalf("bad token must answer bad_token with reason/status: %+v", receipt)
	}
	hostSink.mu.Lock()
	closed := hostSink.closed
	hostSink.mu.Unlock()
	if closed {
		t.Fatal("APNs rejection must not close the host connection")
	}
}

func TestPushMalformedPayloadDoesNotCloseHost(t *testing.T) {
	fake := &fakePushSender{}
	h := New(fake)
	hostSink := &fakeSink{}
	host := h.RegisterHost("host-1", "dev", "", "", protocol.Version, 0, hostSink)
	waitFrames(t, hostSink, 1)

	var zero [ChannelIDLen]byte
	if err := h.FromHost(host, protocol.EncodeV2(protocol.FramePush, zero, []byte("{bad"))); err != nil {
		t.Fatalf("malformed push must not return an error killing the socket: %v", err)
	}
	if fake.calls != 0 {
		t.Fatal("undecodable push must not reach APNs")
	}
	hostSink.mu.Lock()
	closed := hostSink.closed
	hostSink.mu.Unlock()
	if closed {
		t.Fatal("malformed push must not close the host connection")
	}
}
