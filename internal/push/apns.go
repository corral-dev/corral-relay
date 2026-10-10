// Package push 把中继收到的推送请求投给苹果推送服务。
//
// 用的是基于 p8 密钥的令牌鉴权（而不是证书）：一把密钥可以同时用于开发与生产
// 环境，也不会像证书那样一年一换。令牌按苹果要求缓存复用，最多一小时换一次——
// 每条推送都重签会被苹果限流。
package push

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/net/http2"
)

const (
	prodHost    = "https://api.push.apple.com"
	sandboxHost = "https://api.sandbox.push.apple.com"
	tokenTTL    = 50 * time.Minute
)

type Config struct {
	KeyPath  string // p8 私钥文件
	KeyID    string
	TeamID   string
	BundleID string
}

// Result is the structured outcome of one APNs delivery attempt:
// 200 means accepted for delivery, which is NOT the same as shown on the phone
// (NSE decrypt, offline devices, user settings are all downstream).
type Result struct {
	StatusCode int
	Reason     string // Apple's verbatim reason string, empty on success
	ApnsID     string // Apple's apns-id, may be empty on failure
}

var (
	ErrBadPushRequest    = errors.New("bad push request")
	ErrPushNotConfigured = errors.New("push not configured")
	ErrBadDeviceToken    = errors.New("bad device token")
	ErrPushRejected      = errors.New("push rejected by Apple")
	ErrPushThrottled     = errors.New("push throttled by Apple")
	ErrPushTransport     = errors.New("push transport error")
)

func (c Config) Enabled() bool {
	return c.KeyPath != "" && c.KeyID != "" && c.TeamID != "" && c.BundleID != ""
}

type Sender struct {
	config Config
	key    *ecdsa.PrivateKey
	client *http.Client

	mu       sync.Mutex
	token    string
	issuedAt time.Time
}

func New(config Config) (*Sender, error) {
	if !config.Enabled() {
		return nil, errors.New("推送配置不完整")
	}
	key, err := loadKey(config.KeyPath)
	if err != nil {
		return nil, err
	}
	transport := &http2.Transport{}
	return &Sender{
		config: config,
		key:    key,
		client: &http.Client{Transport: transport, Timeout: 10 * time.Second},
	}, nil
}

func loadKey(path string) (*ecdsa.PrivateKey, error) {
	data, err := readFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("推送密钥不是合法的 PEM 文件")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("推送密钥类型不对，应为 ECDSA")
	}
	return key, nil
}

func (s *Sender) authToken() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token != "" && time.Since(s.issuedAt) < tokenTTL {
		return s.token, nil
	}
	now := time.Now()
	claims := jwt.MapClaims{"iss": s.config.TeamID, "iat": now.Unix()}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	token.Header["kid"] = s.config.KeyID
	signed, err := token.SignedString(s.key)
	if err != nil {
		return "", err
	}
	s.token, s.issuedAt = signed, now
	return signed, nil
}

// Send delivers one push and returns the structured APNs outcome.
//
// 内容是加密的，所以推送本身必须带 mutable-content，让手机上的通知服务扩展
// 有机会在展示前把它解开；alert 里先放一句无信息量的占位，扩展解密成功后会
// 整条替换掉。用户关掉扩展或解密失败时，看到的就是这句占位——不理想，但绝不
// 会泄露内容，这个取舍是刻意的。
//
// The returned Result only means accepted by APNs (200 = accepted), never that
// the phone displayed it. Callers must translate errors into receipts honestly:
// never treat "enqueued" as success, and never close the host connection
// because a push failed.
func (s *Sender) Send(token, env, hostID string, payload []byte) (Result, error) {
	if token == "" {
		return Result{}, ErrBadPushRequest
	}
	auth, err := s.authToken()
	if err != nil {
		return Result{}, fmt.Errorf("%w: mint auth token: %v", ErrPushTransport, err)
	}
	host := prodHost
	if env == "sandbox" {
		host = sandboxHost
	}
	body := buildPayload(hostID, payload)
	request, err := http.NewRequestWithContext(
		context.Background(), http.MethodPost, fmt.Sprintf("%s/3/device/%s", host, token), bytes.NewReader(body),
	)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrPushTransport, err)
	}
	request.Header.Set("authorization", "bearer "+auth)
	request.Header.Set("apns-topic", s.config.BundleID)
	request.Header.Set("apns-push-type", "alert")
	request.Header.Set("apns-priority", "10")
	request.Header.Set("content-type", "application/json")

	response, err := s.client.Do(request)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrPushTransport, err)
	}
	defer response.Body.Close()
	limited, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	reason := apnsReason(limited)
	apnsID := response.Header.Get("apns-id")
	result := Result{StatusCode: response.StatusCode, Reason: reason, ApnsID: apnsID}
	// Provider success is HTTP 200 only
	// (https://developer.apple.com/documentation/usernotifications/handling-notification-responses-from-apns):
	// any other 2xx is unexpected and treated as rejected, never as accepted.
	switch {
	case response.StatusCode == http.StatusOK:
		return result, nil
	case response.StatusCode == http.StatusTooManyRequests:
		return result, fmt.Errorf("%w: %d reason=%s", ErrPushThrottled, response.StatusCode, reason)
	case response.StatusCode >= 500:
		return result, fmt.Errorf("%w: apple push returned %d reason=%s", ErrPushTransport, response.StatusCode, reason)
	case response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusGone:
		if reason == "BadDeviceToken" || reason == "Unregistered" {
			return result, fmt.Errorf("%w: %d reason=%s", ErrBadDeviceToken, response.StatusCode, reason)
		}
		return result, fmt.Errorf("%w: %d reason=%s", ErrPushRejected, response.StatusCode, reason)
	default:
		return result, fmt.Errorf("%w: apple push returned %d reason=%s", ErrPushRejected, response.StatusCode, reason)
	}
}

// apnsReason extracts the verbatim reason from an APNs error body; empty when
// the body is absent or not JSON.
func apnsReason(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var parsed struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ""
	}
	return parsed.Reason
}
