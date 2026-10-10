package hub

import (
	"encoding/json"
	"errors"

	"github.com/corral-dev/corral-relay/internal/push"
)

// pushRequest 是开发机请中继代发推送时的载荷。
//
// Payload 是一段 base64 的密文，中继解不开也不该去解——里面是会话标题和摘要，
// 属于用户内容。中继只关心把它原样塞进苹果推送的自定义字段里。
// ID is the host-generated correlation id (empty from old hosts):
// the relay echoes it back in the receipt.
type pushRequest struct {
	Token   string `json:"token"`
	Env     string `json:"env"`
	Payload string `json:"payload"`
	ID      string `json:"id"`
}

func decodePushRequest(raw []byte) (pushRequest, error) {
	var request pushRequest
	err := json.Unmarshal(raw, &request)
	return request, err
}

// pushReceipt is the relay-to-host push routing receipt (FRAME_PUSH_RECEIPT).
//
// ok=true only means accepted by APNs (HTTP 200 accepted), never shown on phone.
type pushReceipt struct {
	ID     string `json:"id"`
	Ok     bool   `json:"ok"`
	Code   string `json:"code"`
	Status int    `json:"status"`
	Reason string `json:"reason"`
	ApnsID string `json:"apns_id"`
}

func encodePushReceipt(receipt pushReceipt) []byte {
	body, _ := json.Marshal(receipt)
	return body
}

// receiptFor translates a push outcome into the stable receipt code vocabulary.
func receiptFor(id string, result push.Result, err error) pushReceipt {
	if err == nil {
		return pushReceipt{ID: id, Ok: true, Code: "ok", Status: result.StatusCode, ApnsID: result.ApnsID}
	}
	code := "internal"
	switch {
	case errors.Is(err, push.ErrPushNotConfigured):
		code = "no_push_config"
	case errors.Is(err, push.ErrBadPushRequest):
		code = "bad_request"
	case errors.Is(err, push.ErrBadDeviceToken):
		code = "bad_token"
	case errors.Is(err, push.ErrPushThrottled):
		code = "throttled"
	case errors.Is(err, push.ErrPushTransport):
		code = "transport"
	case errors.Is(err, push.ErrPushRejected):
		code = "rejected"
	}
	return pushReceipt{ID: id, Ok: false, Code: code, Status: result.StatusCode, Reason: result.Reason, ApnsID: result.ApnsID}
}
