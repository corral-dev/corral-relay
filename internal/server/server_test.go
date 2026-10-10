package server

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/corral-dev/corral-relay/internal/hub"
	"github.com/corral-dev/corral-relay/internal/protocol"
	"github.com/corral-dev/corral-relay/internal/store"
	"github.com/corral-dev/corral-relay/internal/store/file"
)

func testServer(t *testing.T) (*Server, *file.Store) {
	t.Helper()
	st, err := file.Open(filepath.Join(t.TempDir(), "hosts.json"))
	if err != nil {
		t.Fatalf("打开状态文件失败: %v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(hub.New(nil), st, log, Options{}), st
}

func TestHandleHostAuthenticatesLegacyHeaders(t *testing.T) {
	srv, st := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/host", nil)
	req.Header.Set("X-Pickup-Host", "host-legacy")
	req.Header.Set("X-Pickup-Host-Name", "旧开发机")
	req.Header.Set("Authorization", "Bearer tok-legacy")
	rec := httptest.NewRecorder()
	srv.handleHostV1(rec, req)

	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("旧请求头应当能通过认证，得到 %d %s", rec.Code, rec.Body.String())
	}
	host, ok := st.Lookup("host-legacy")
	if !ok {
		t.Fatal("认证通过后应当记下这台开发机")
	}
	if host.Name != "旧开发机" {
		t.Fatalf("旧名字头没被读到: %q", host.Name)
	}
	sum := sha256.Sum256([]byte("tok-legacy"))
	if host.TokenHash != hex.EncodeToString(sum[:]) {
		t.Fatal("登记的凭据哈希对不上")
	}
}

func TestHandleHostPrefersNewHeaders(t *testing.T) {
	srv, st := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/host", nil)
	req.Header.Set("X-Corral-Host", "host-new")
	req.Header.Set("X-Corral-Host-Name", "新开发机")
	req.Header.Set("X-Pickup-Host", "host-legacy")
	req.Header.Set("X-Pickup-Host-Name", "旧开发机")
	req.Header.Set("Authorization", "Bearer tok-new")
	rec := httptest.NewRecorder()
	srv.handleHostV1(rec, req)

	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("新请求头应当能通过认证，得到 %d %s", rec.Code, rec.Body.String())
	}
	if _, ok := st.Lookup("host-new"); !ok {
		t.Fatal("有新请求头时应当用新标识登记")
	}
	if _, ok := st.Lookup("host-legacy"); ok {
		t.Fatal("新旧请求头同时出现时不该再按旧标识登记")
	}
}

func TestHandleHostRejectsMissingHostHeader(t *testing.T) {
	srv, _ := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/host", nil)
	req.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	srv.handleHostV1(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("缺开发机标识应当 401，得到 %d", rec.Code)
	}
}

func TestHandleHostV2RejectsBadAssertion(t *testing.T) {
	srv, _ := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v2/host", nil)
	req.Header.Set("X-Corral-Auth", "nope")
	rec := httptest.NewRecorder()
	srv.handleHostV2(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("坏断言应当 401，得到 %d", rec.Code)
	}
}

func TestDeviceOfflineHost(t *testing.T) {
	srv, _ := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v2/device?host=missing", nil)
	rec := httptest.NewRecorder()
	srv.handleDeviceV2(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("离线开发机应当 503，得到 %d", rec.Code)
	}
}

func TestClientIPTrustsForwardedOnlyFromProxy(t *testing.T) {
	fromLoopback := httptest.NewRequest(http.MethodGet, "/", nil)
	fromLoopback.RemoteAddr = "127.0.0.1:12345"
	fromLoopback.Header.Set("X-Forwarded-For", "203.0.113.9, 10.0.0.1")
	if got := clientIP(fromLoopback); got != "203.0.113.9" {
		t.Fatalf("环回应读转发地址，得到 %s", got)
	}

	fromPublic := httptest.NewRequest(http.MethodGet, "/", nil)
	fromPublic.RemoteAddr = "198.51.100.2:443"
	fromPublic.Header.Set("X-Forwarded-For", "203.0.113.9")
	if got := clientIP(fromPublic); got != "198.51.100.2" {
		t.Fatalf("公网对端不得信任伪造转发头，得到 %s", got)
	}
}

func TestVerifyHostV2RejectsOfflineHijack(t *testing.T) {
	srv, st := testServer(t)
	oldPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AuthenticateV2("rid-1", "机", oldPub, false, true, false); err != nil {
		t.Fatal(err)
	}
	newPub, newPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	nonce := bytes(16, 7)
	req := httptest.NewRequest(http.MethodGet, "/v2/host", nil)
	req.Header.Set("X-Corral-Host-Pub", hex.EncodeToString(newPub))
	req.Header.Set("X-Corral-Auth", signAuth(newPriv, "rid-1", now, nonce))
	parsed, err := protocol.ParseAssertion(req.Header.Get("X-Corral-Auth"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.verifyHostV2(req, parsed); err != store.ErrKeyMismatch {
		t.Fatalf("没有旧钥匙证明时离线换钥匙应当被拒，得到 %v", err)
	}
}

func TestVerifyHostV2RotatesWithPrevAuth(t *testing.T) {
	srv, st := testServer(t)
	oldPub, oldPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AuthenticateV2("rid-1", "机", oldPub, false, true, false); err != nil {
		t.Fatal(err)
	}
	newPub, newPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	nonce := bytes(16, 3)
	req := httptest.NewRequest(http.MethodGet, "/v2/host", nil)
	req.Header.Set("X-Corral-Host-Pub", hex.EncodeToString(newPub))
	req.Header.Set("X-Corral-Auth", signAuth(newPriv, "rid-1", now, nonce))
	req.Header.Set("X-Corral-Prev-Auth", signAuth(oldPriv, "rid-1", now, nonce))
	parsed, err := protocol.ParseAssertion(req.Header.Get("X-Corral-Auth"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := srv.verifyHostV2(req, parsed)
	if err != nil {
		t.Fatalf("带旧钥匙证明应当能轮换: %v", err)
	}
	if subtle.ConstantTimeCompare(got, newPub) != 1 {
		t.Fatal("轮换后应当用新公钥")
	}
	host, ok := st.Lookup("rid-1")
	if !ok || subtle.ConstantTimeCompare(host.Ed25519Pub, newPub) != 1 {
		t.Fatal("存档应当换成新钥匙")
	}
}

func signAuth(priv ed25519.PrivateKey, rid string, ts int64, nonce []byte) string {
	sig := ed25519.Sign(priv, protocol.SignMessage(rid, ts, nonce))
	return protocol.FormatAssertion(rid, ts, nonce, sig)
}

func bytes(n, fill int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte(fill)
	}
	return out
}
