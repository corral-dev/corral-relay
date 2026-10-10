package push

import (
	"encoding/json"
	"testing"
)

func TestBuildPayloadKeepsLegacyField(t *testing.T) {
	body := buildPayload("host-1", []byte("密文"))
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("推送体不是 JSON: %v", err)
	}
	corral, _ := parsed["corral"].(string)
	legacy, _ := parsed["pickup"].(string)
	if corral != "密文" || legacy != "密文" {
		t.Fatalf("新旧字段都应带同一段密文，corral=%q pickup=%q", corral, legacy)
	}
	if parsed["host_id"] != "host-1" {
		t.Fatalf("host_id 丢了: %#v", parsed["host_id"])
	}
}
