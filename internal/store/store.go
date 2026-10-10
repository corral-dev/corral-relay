// Package store 保存中继需要记住的极少量状态。
//
// 中继刻意不存会话、消息、设备清单。接口让单租户（JSON 文件）和多租户
// （Postgres）走同一套调用，调用方不关心底下是哪种。
package store

import (
	"encoding/hex"
	"errors"
	"time"
)

var (
	ErrTokenMismatch   = errors.New("注册凭据不匹配")
	ErrKeyMismatch     = errors.New("开发机注册密钥不匹配")
	ErrHostUnknown     = errors.New("开发机尚未登记")
	ErrHostBanned      = errors.New("开发机已被封禁")
	ErrAccountMismatch = errors.New("这台开发机属于另一个账号")
	ErrNotSupported    = errors.New("当前运行模式不支持此操作")
	ErrUnauthorized    = errors.New("凭据无效")
	ErrDevicePending   = errors.New("authorization_pending")
	ErrDeviceExpired   = errors.New("设备码已过期")
	ErrDeviceDenied    = errors.New("access_denied")
	ErrQuota           = errors.New("超出配额")
)

// Host 是一台已登记开发机。
type Host struct {
	ID           string
	Name         string
	TokenHash    string // 仅 v1
	Ed25519Pub   []byte // v2 原始 32 字节
	AccountID    string
	NodeID       string
	BundleID     string
	BannedAt     *time.Time
	RegisteredAt int64
	LastSeenAt   int64
}

func (h *Host) Banned() bool {
	return h != nil && h.BannedAt != nil
}

func (h *Host) PubHex() string {
	if h == nil || len(h.Ed25519Pub) == 0 {
		return ""
	}
	return hex.EncodeToString(h.Ed25519Pub)
}

type Account struct {
	ID       string
	Login    string
	Provider string
}

type Quota struct {
	MaxHosts          int
	MaxDevicesPerHost int
	MonthlyBytes      int64
	DailyPushes       int
	HostsUsed         int
	BytesUsed         int64
	PushesToday       int
}

func (q Quota) Unlimited() bool {
	return q.MaxHosts <= 0 && q.MonthlyBytes <= 0
}

type DeviceCode struct {
	DeviceCode string
	UserCode   string
	AccountID  string
	ExpiresAt  time.Time
	Interval   int
	Approved   bool
}

type Store interface {
	Lookup(id string) (*Host, bool)
	AuthenticateV1(id, name, tokenHash string, online bool) (*Host, error)
	// AuthenticateV2 校验 v2 开发机。
	//
	// tofu：单租户首次见到即登记。
	// replaceWhenOffline 已废弃：离线也不允许用另一把钥匙抢走坑位。
	// 轮换走 RotateEd25519（须先验证旧钥匙签名）。
	// 多租户必须已经 POST /v2/hosts 登记过，tofu 为 false。
	AuthenticateV2(id, name string, pub []byte, online, tofu, replaceWhenOffline bool) (*Host, error)
	RotateEd25519(id string, pub []byte) error
	RegisterHost(accountID, id, name string, pub []byte, bundleID string) (*Host, error)
	SetNode(id, nodeID string) error
	TouchSeen(id, name string) error
	Ban(id string, banned bool) error
	ListHosts(accountID string) ([]*Host, error)
	Count() int
	Flush() error

	UpsertAccount(provider, providerID, login string) (*Account, error)
	CreateCLIToken(accountID string) (string, error)
	AccountByToken(token string) (*Account, error)
	CreateDeviceCode(userCode, deviceCode string, ttl time.Duration) (*DeviceCode, error)
	LookupDeviceCode(deviceCode string) (*DeviceCode, error)
	LookupUserCode(userCode string) (*DeviceCode, error)
	ApproveDeviceCode(userCode, accountID string) error
	ConsumeDeviceCode(deviceCode string) (*Account, string, error)

	QuotaFor(accountID string) Quota
	SetQuotaDefaults(q Quota)
	AddBytes(accountID string, n int64)
	AddPushes(accountID string, n int)
}
