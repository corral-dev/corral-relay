package file

import (
	"path/filepath"
	"testing"

	"github.com/corral-dev/corral-relay/internal/store"
)

func TestV2TofuThenRejectsWrongKeyWhileOnline(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "hosts.json"))
	if err != nil {
		t.Fatal(err)
	}
	pub := bytes(32, 1)
	host, err := st.AuthenticateV2("rid-1", "机", pub, false, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if host.ID != "rid-1" {
		t.Fatalf("id=%s", host.ID)
	}
	_, err = st.AuthenticateV2("rid-1", "机", bytes(32, 2), true, true, true)
	if err != store.ErrKeyMismatch {
		t.Fatalf("在线换密钥应当被拒，得到 %v", err)
	}
}

func TestV2RejectsReplaceWhenOffline(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "hosts.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.AuthenticateV2("rid-1", "机", bytes(32, 1), false, true, true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.AuthenticateV2("rid-1", "机", bytes(32, 2), false, true, true)
	if err != store.ErrKeyMismatch {
		t.Fatalf("离线换钥匙应当被拒，得到 %v", err)
	}
}

func TestRotateEd25519UpdatesKey(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "hosts.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.AuthenticateV2("rid-1", "机", bytes(32, 1), false, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RotateEd25519("rid-1", bytes(32, 2)); err != nil {
		t.Fatal(err)
	}
	host, err := st.AuthenticateV2("rid-1", "机", bytes(32, 2), false, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if host.Ed25519Pub[0] != 2 {
		t.Fatal("轮换后应当用新钥匙")
	}
}

func TestV2WithoutTofuRequiresPriorRegistration(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "hosts.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.AuthenticateV2("rid-1", "机", bytes(32, 1), false, false, false)
	if err != store.ErrHostUnknown {
		t.Fatalf("多租户未登记应当未知，得到 %v", err)
	}
}

func TestFlushRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts.json")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AuthenticateV2("rid-1", "苏州", bytes(32, 9), false, true, true); err != nil {
		t.Fatal(err)
	}
	if err := st.Flush(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	host, ok := reopened.Lookup("rid-1")
	if !ok || host.Name != "苏州" || host.Ed25519Pub[0] != 9 {
		t.Fatalf("落盘往返失败: %#v", host)
	}
}

func bytes(n, fill int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte(fill)
	}
	return out
}
