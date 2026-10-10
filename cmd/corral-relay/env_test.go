package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnvOrFirstPrefersNewName(t *testing.T) {
	t.Setenv("CORRAL_RELAY_ADDR", "new:1")
	t.Setenv("PICKUP_RELAY_ADDR", "old:1")
	got := envOrFirst("CORRAL_RELAY_ADDR", "PICKUP_RELAY_ADDR", ":8788")
	if got != "new:1" {
		t.Fatalf("新名字在时应当优先，得到 %q", got)
	}
}

func TestEnvOrFirstFallsBackToOldName(t *testing.T) {
	t.Setenv("CORRAL_RELAY_ADDR", "")
	t.Setenv("PICKUP_RELAY_ADDR", "old:1")
	got := envOrFirst("CORRAL_RELAY_ADDR", "PICKUP_RELAY_ADDR", ":8788")
	if got != "old:1" {
		t.Fatalf("新名字空时应当沿用旧名字，得到 %q", got)
	}
}

func TestEnvOrFirstUsesDefault(t *testing.T) {
	t.Setenv("CORRAL_PUSH_KEY", "")
	t.Setenv("PICKUP_PUSH_KEY", "")
	got := envOrFirst("CORRAL_PUSH_KEY", "PICKUP_PUSH_KEY", "")
	if got != "" {
		t.Fatalf("都没设时应当落到默认值，得到 %q", got)
	}
}

func TestResolveStatePathUsesLegacyWhenDefaultMissing(t *testing.T) {
	root := t.TempDir()
	def := filepath.Join(root, "corral", "hosts.json")
	legacy := filepath.Join(root, "pickup", "hosts.json")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := resolveStatePath(def, def, legacy)
	if got != legacy {
		t.Fatalf("新默认文件不存在时应沿用旧表，得到 %s", got)
	}
}

func TestResolveStatePathKeepsExplicitPath(t *testing.T) {
	root := t.TempDir()
	def := filepath.Join(root, "corral", "hosts.json")
	legacy := filepath.Join(root, "pickup", "hosts.json")
	explicit := filepath.Join(root, "custom", "hosts.json")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := resolveStatePath(explicit, def, legacy)
	if got != explicit {
		t.Fatalf("显式路径不该被旧默认目录抢走，得到 %s", got)
	}
}

func TestResolveStatePathPrefersExistingDefault(t *testing.T) {
	root := t.TempDir()
	def := filepath.Join(root, "corral", "hosts.json")
	legacy := filepath.Join(root, "pickup", "hosts.json")
	if err := os.MkdirAll(filepath.Dir(def), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(def, []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := resolveStatePath(def, def, legacy)
	if got != def {
		t.Fatalf("新文件在时应当用新路径，得到 %s", got)
	}
}
