package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	_ "embed"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/corral-dev/corral-relay/internal/store"
)

//go:embed schema.sql
var schemaSQL string

type Store struct {
	pool  *pgxpool.Pool
	quota store.Quota
}

func Open(ctx context.Context, url string) (*Store, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	if _, err := pool.Exec(ctx, schemaSQL); err != nil {
		pool.Close()
		return nil, fmt.Errorf("迁移失败: %w", err)
	}
	return &Store{
		pool: pool,
		quota: store.Quota{
			MaxHosts:          5,
			MaxDevicesPerHost: 16,
			MonthlyBytes:      50 << 30,
			DailyPushes:       500,
		},
	}, nil
}

func (s *Store) Close() { s.pool.Close() }

func (s *Store) Lookup(id string) (*store.Host, bool) {
	h, err := s.lookup(context.Background(), id)
	if err != nil {
		return nil, false
	}
	return h, true
}

func (s *Store) lookup(ctx context.Context, id string) (*store.Host, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, account_id, name, ed25519_pub, node_id, bundle_id, banned_at,
		       EXTRACT(EPOCH FROM registered_at)::bigint, EXTRACT(EPOCH FROM last_seen_at)::bigint
		FROM hosts WHERE id = $1`, id)
	return scanHost(row)
}

type scanner interface {
	Scan(dest ...any) error
}

func scanHost(row scanner) (*store.Host, error) {
	h := &store.Host{}
	var banned *time.Time
	if err := row.Scan(&h.ID, &h.AccountID, &h.Name, &h.Ed25519Pub, &h.NodeID, &h.BundleID, &banned, &h.RegisteredAt, &h.LastSeenAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, store.ErrHostUnknown
		}
		return nil, err
	}
	h.BannedAt = banned
	return h, nil
}

func (s *Store) AuthenticateV1(string, string, string, bool) (*store.Host, error) {
	return nil, store.ErrNotSupported
}

func (s *Store) AuthenticateV2(id, name string, pub []byte, _, _, _ bool) (*store.Host, error) {
	ctx := context.Background()
	host, err := s.lookup(ctx, id)
	if err != nil {
		return nil, err
	}
	if host.Banned() {
		return nil, store.ErrHostBanned
	}
	if subtleNeq(host.Ed25519Pub, pub) {
		return nil, store.ErrKeyMismatch
	}
	_ = s.TouchSeen(id, name)
	return host, nil
}

func (s *Store) RotateEd25519(string, []byte) error {
	return store.ErrNotSupported
}

func subtleNeq(a, b []byte) bool {
	if len(a) != len(b) {
		return true
	}
	var v byte
	for i := range a {
		v |= a[i] ^ b[i]
	}
	return v != 0
}

func (s *Store) RegisterHost(accountID, id, name string, pub []byte, bundleID string) (*store.Host, error) {
	ctx := context.Background()
	existing, err := s.lookup(ctx, id)
	if err == nil {
		if existing.AccountID != accountID {
			return nil, store.ErrAccountMismatch
		}
		_, err = s.pool.Exec(ctx, `
			UPDATE hosts SET name=$2, ed25519_pub=$3, bundle_id=$4, last_seen_at=now()
			WHERE id=$1`, id, name, pub, bundleID)
		if err != nil {
			return nil, err
		}
		return s.lookup(ctx, id)
	}
	if !errors.Is(err, store.ErrHostUnknown) {
		return nil, err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO hosts (id, account_id, name, ed25519_pub, bundle_id)
		VALUES ($1,$2,$3,$4,$5)`, id, accountID, name, pub, bundleID)
	if err != nil {
		return nil, err
	}
	return s.lookup(ctx, id)
}

func (s *Store) SetNode(id, nodeID string) error {
	_, err := s.pool.Exec(context.Background(), `UPDATE hosts SET node_id=$2 WHERE id=$1`, id, nodeID)
	return err
}

func (s *Store) TouchSeen(id, name string) error {
	_, err := s.pool.Exec(context.Background(), `
		UPDATE hosts SET last_seen_at=now(), name=CASE WHEN $2='' THEN name ELSE $2 END WHERE id=$1`, id, name)
	return err
}

func (s *Store) Ban(id string, banned bool) error {
	if banned {
		_, err := s.pool.Exec(context.Background(), `UPDATE hosts SET banned_at=now() WHERE id=$1`, id)
		return err
	}
	_, err := s.pool.Exec(context.Background(), `UPDATE hosts SET banned_at=NULL WHERE id=$1`, id)
	return err
}

func (s *Store) ListHosts(accountID string) ([]*store.Host, error) {
	rows, err := s.pool.Query(context.Background(), `
		SELECT id, account_id, name, ed25519_pub, node_id, bundle_id, banned_at,
		       EXTRACT(EPOCH FROM registered_at)::bigint, EXTRACT(EPOCH FROM last_seen_at)::bigint
		FROM hosts WHERE account_id=$1 ORDER BY last_seen_at DESC`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*store.Host
	for rows.Next() {
		h, err := scanHost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *Store) Count() int {
	var n int
	_ = s.pool.QueryRow(context.Background(), `SELECT count(*) FROM hosts`).Scan(&n)
	return n
}

func (s *Store) Flush() error { return nil }

func (s *Store) UpsertAccount(provider, providerID, login string) (*store.Account, error) {
	ctx := context.Background()
	var existing store.Account
	err := s.pool.QueryRow(ctx, `
		SELECT id, login, provider FROM accounts WHERE provider=$1 AND provider_id=$2`,
		provider, providerID).Scan(&existing.ID, &existing.Login, &existing.Provider)
	if err == nil {
		_, _ = s.pool.Exec(ctx, `UPDATE accounts SET login=$2 WHERE id=$1`, existing.ID, login)
		existing.Login = login
		return &existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	acc := store.Account{ID: newID(), Login: login, Provider: provider}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO accounts (id, login, provider, provider_id) VALUES ($1,$2,$3,$4)`,
		acc.ID, login, provider, providerID)
	return &acc, err
}

func (s *Store) CreateCLIToken(accountID string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO cli_tokens (token_hash, account_id) VALUES ($1,$2)`, hex.EncodeToString(sum[:]), accountID)
	return token, err
}

func (s *Store) AccountByToken(token string) (*store.Account, error) {
	sum := sha256.Sum256([]byte(token))
	var acc store.Account
	err := s.pool.QueryRow(context.Background(), `
		SELECT a.id, a.login, a.provider FROM cli_tokens t
		JOIN accounts a ON a.id = t.account_id
		WHERE t.token_hash=$1`, hex.EncodeToString(sum[:])).Scan(&acc.ID, &acc.Login, &acc.Provider)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrUnauthorized
	}
	return &acc, err
}

func (s *Store) CreateDeviceCode(userCode, deviceCode string, ttl time.Duration) (*store.DeviceCode, error) {
	exp := time.Now().Add(ttl)
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO device_flow_codes (device_code, user_code, expires_at) VALUES ($1,$2,$3)`,
		deviceCode, userCode, exp)
	if err != nil {
		return nil, err
	}
	return &store.DeviceCode{DeviceCode: deviceCode, UserCode: userCode, ExpiresAt: exp, Interval: 5}, nil
}

func (s *Store) LookupDeviceCode(deviceCode string) (*store.DeviceCode, error) {
	return s.lookupCode(`SELECT device_code, user_code, COALESCE(account_id,''), expires_at, interval_s, approved FROM device_flow_codes WHERE device_code=$1`, deviceCode)
}

func (s *Store) LookupUserCode(userCode string) (*store.DeviceCode, error) {
	return s.lookupCode(`SELECT device_code, user_code, COALESCE(account_id,''), expires_at, interval_s, approved FROM device_flow_codes WHERE user_code=$1`, userCode)
}

func (s *Store) lookupCode(q, key string) (*store.DeviceCode, error) {
	d := &store.DeviceCode{}
	err := s.pool.QueryRow(context.Background(), q, key).Scan(&d.DeviceCode, &d.UserCode, &d.AccountID, &d.ExpiresAt, &d.Interval, &d.Approved)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrUnauthorized
	}
	return d, err
}

func (s *Store) ApproveDeviceCode(userCode, accountID string) error {
	tag, err := s.pool.Exec(context.Background(), `
		UPDATE device_flow_codes SET approved=true, account_id=$2 WHERE user_code=$1 AND expires_at > now()`, userCode, accountID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return store.ErrDeviceExpired
	}
	return nil
}

func (s *Store) ConsumeDeviceCode(deviceCode string) (*store.Account, string, error) {
	ctx := context.Background()
	d, err := s.LookupDeviceCode(deviceCode)
	if err != nil {
		return nil, "", err
	}
	if time.Now().After(d.ExpiresAt) {
		return nil, "", store.ErrDeviceExpired
	}
	if !d.Approved || d.AccountID == "" {
		return nil, "", store.ErrDevicePending
	}
	_, _ = s.pool.Exec(ctx, `DELETE FROM device_flow_codes WHERE device_code=$1`, deviceCode)
	var acc store.Account
	err = s.pool.QueryRow(ctx, `SELECT id, login, provider FROM accounts WHERE id=$1`, d.AccountID).
		Scan(&acc.ID, &acc.Login, &acc.Provider)
	if err != nil {
		return nil, "", err
	}
	token, err := s.CreateCLIToken(acc.ID)
	return &acc, token, err
}

func (s *Store) QuotaFor(accountID string) store.Quota {
	q := s.quota
	month := time.Now().UTC().Format("2006-01")
	today := time.Now().UTC().Format("2006-01-02")
	var bytes int64
	var pushes int
	var pushesDay *time.Time
	_ = s.pool.QueryRow(context.Background(), `
		SELECT bytes, pushes, pushes_day FROM usage_counters WHERE account_id=$1 AND month=$2`,
		accountID, month).Scan(&bytes, &pushes, &pushesDay)
	q.BytesUsed = bytes
	if pushesDay != nil && pushesDay.UTC().Format("2006-01-02") == today {
		q.PushesToday = pushes
	}
	var hosts int
	_ = s.pool.QueryRow(context.Background(), `SELECT count(*) FROM hosts WHERE account_id=$1`, accountID).Scan(&hosts)
	q.HostsUsed = hosts
	return q
}

func (s *Store) SetQuotaDefaults(q store.Quota) { s.quota = q }

func (s *Store) AddBytes(accountID string, n int64) {
	month := time.Now().UTC().Format("2006-01")
	_, _ = s.pool.Exec(context.Background(), `
		INSERT INTO usage_counters (account_id, month, bytes) VALUES ($1,$2,$3)
		ON CONFLICT (account_id) DO UPDATE SET
			bytes = CASE WHEN usage_counters.month = EXCLUDED.month THEN usage_counters.bytes + EXCLUDED.bytes ELSE EXCLUDED.bytes END,
			month = EXCLUDED.month`, accountID, month, n)
}

func (s *Store) AddPushes(accountID string, n int) {
	month := time.Now().UTC().Format("2006-01")
	_, _ = s.pool.Exec(context.Background(), `
		INSERT INTO usage_counters (account_id, month, pushes, pushes_day, bytes) VALUES ($1,$2,$3,CURRENT_DATE,0)
		ON CONFLICT (account_id) DO UPDATE SET
			pushes = CASE WHEN usage_counters.pushes_day = CURRENT_DATE THEN usage_counters.pushes + EXCLUDED.pushes ELSE EXCLUDED.pushes END,
			pushes_day = CURRENT_DATE,
			month = EXCLUDED.month`, accountID, month, n)
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
