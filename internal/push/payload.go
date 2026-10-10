package push

import (
	"encoding/json"
	"os"
)

func readFile(path string) ([]byte, error) { return os.ReadFile(path) }

// buildPayload 拼出发给苹果的推送体。
//
// alert 里那句话是占位：真正的标题与正文在自定义字段里，是密文，由手机上的
// 通知服务扩展解开后整条替换。mutable-content 必须为 1，否则扩展根本不会被唤起。
// host_id 是明文——扩展需要它去本地钥匙串取对应开发机公钥，内容本身仍是密文。
// corral 与 pickup 两份字段内容相同：新扩展读前者，已装的旧扩展读后者。
func buildPayload(hostID string, sealed []byte) []byte {
	cipher := string(sealed)
	body := map[string]any{
		"aps": map[string]any{
			"alert": map[string]any{
				"title": "corral",
				// Placeholder only — NSE replaces title/body after decrypt.
				// Covers waiting and session-end (completed / aborted) alerts.
				"body": "Session update",
			},
			"mutable-content": 1,
			"sound":           "default",
		},
		"corral":  cipher,
		"pickup":  cipher,
		"host_id": hostID,
	}
	data, _ := json.Marshal(body)
	return data
}
