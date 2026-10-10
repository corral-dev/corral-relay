package protocol

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"
)

func TestRoutingIDIsStableLowercaseBase32(t *testing.T) {
	pub := make([]byte, 32)
	for i := range pub {
		pub[i] = byte(i)
	}
	got := RoutingID(pub)
	if got != "xp47bhr5gnbtbbmjviugjxbnxm" {
		t.Fatalf("routing_id 与规范向量不一致: %s", got)
	}
	if len(got) != 26 {
		t.Fatalf("长度应为 26，得到 %d", len(got))
	}
}

func TestV2FrameRoundTrip(t *testing.T) {
	var id [ChannelIDLen]byte
	for i := range id {
		id[i] = byte(i)
	}
	raw := EncodeV2(FrameData, id, []byte("payload"))
	if raw[0] != Version || raw[1] != FrameData {
		t.Fatalf("帧头不对: %x %x", raw[0], raw[1])
	}
	kind, gotID, payload, err := DecodeV2(raw)
	if err != nil {
		t.Fatal(err)
	}
	if kind != FrameData || gotID != id || string(payload) != "payload" {
		t.Fatalf("往返失败 kind=%x payload=%q", kind, payload)
	}
}

func TestDecodeV2RejectsV1Shape(t *testing.T) {
	var id [ChannelIDLen]byte
	raw := EncodeV1(FrameData, id, []byte("x"))
	if _, _, _, err := DecodeV2(raw); err != ErrBadVersion {
		t.Fatalf("v1 帧应当被 v2 解码拒绝，得到 %v", err)
	}
}

func TestAssertionSignAndVerify(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, NonceSize)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	ts := time.Now().Unix()
	rid := "xp47bhr5gnbtbbmjviugjxbnxm"
	sig := ed25519.Sign(priv, SignMessage(rid, ts, nonce))
	header := FormatAssertion(rid, ts, nonce, sig)
	parsed, err := ParseAssertion(header)
	if err != nil {
		t.Fatal(err)
	}
	cache := NewNonceCache()
	if err := VerifyAssertion(pub, parsed, time.Now(), cache); err != nil {
		t.Fatalf("合法断言应当通过: %v", err)
	}
	if err := VerifyAssertion(pub, parsed, time.Now(), cache); err != ErrAuthReplay {
		t.Fatalf("重放应当被拒，得到 %v", err)
	}
}

func TestAssertionRejectsClockSkew(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, NonceSize)
	_, _ = rand.Read(nonce)
	ts := time.Now().Add(-2 * time.Minute).Unix()
	sig := ed25519.Sign(priv, SignMessage("abc", ts, nonce))
	parsed, err := ParseAssertion(FormatAssertion("abc", ts, nonce, sig))
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyAssertion(pub, parsed, time.Now(), NewNonceCache()); err != ErrAuthSkew {
		t.Fatalf("时钟偏差应当被拒，得到 %v", err)
	}
}

func TestAssertionRejectsWrongKey(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, NonceSize)
	_, _ = rand.Read(nonce)
	ts := time.Now().Unix()
	sig := ed25519.Sign(priv, SignMessage("abc", ts, nonce))
	parsed, _ := ParseAssertion(FormatAssertion("abc", ts, nonce, sig))
	if err := VerifyAssertion(other, parsed, time.Now(), NewNonceCache()); err != ErrBadSignature {
		t.Fatalf("错密钥应当被拒，得到 %v", err)
	}
}

func TestAttachAssertionRoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, NonceSize)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	ts := time.Now().Unix()
	rid := "xp47bhr5gnbtbbmjviugjxbnxm"
	msg := SignAttachMessage(rid, "node-a", 7, LaneBulk, ts, nonce)
	sig := ed25519.Sign(priv, msg)
	header := FormatAttachAssertion(rid, "node-a", 7, LaneBulk, ts, nonce, sig)
	parsed, err := ParseAttachAssertion(header)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Generation != 7 || parsed.Lane != LaneBulk || parsed.NodeID != "node-a" {
		t.Fatalf("附着断言字段不对: %+v", parsed)
	}
	cache := NewNonceCache()
	if err := VerifyAttachAssertion(pub, parsed, time.Now(), cache); err != nil {
		t.Fatalf("合法附着断言应当通过: %v", err)
	}
	if err := VerifyAttachAssertion(pub, parsed, time.Now(), cache); err != ErrAuthReplay {
		t.Fatalf("附着重放应当被拒，得到 %v", err)
	}
}

func TestNormalizeLane(t *testing.T) {
	got, err := NormalizeLane("")
	if err != nil || got != LaneInteraction {
		t.Fatalf("空 lane 应为 interaction，得到 %q %v", got, err)
	}
	if _, err := NormalizeLane("other"); err != ErrBadLane {
		t.Fatalf("非法 lane 应当被拒，得到 %v", err)
	}
}
