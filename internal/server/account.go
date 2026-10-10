package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/corral-dev/corral-relay/internal/store"
	"github.com/corral-dev/corral-relay/internal/web"
)

func (s *Server) handleAuthDevice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	userCode := newUserCode()
	deviceCode := newHex(32)
	row, err := s.store.CreateDeviceCode(userCode, deviceCode, 15*time.Minute)
	if err != nil {
		http.Error(w, "无法签发设备码", http.StatusInternalServerError)
		return
	}
	verify := strings.TrimRight(s.opts.PublicURL, "/") + "/device"
	writeJSON(w, http.StatusOK, map[string]any{
		"device_code":      row.DeviceCode,
		"user_code":        row.UserCode,
		"verification_uri": verify,
		"expires_in":       900,
		"interval":         5,
	})
}

func (s *Server) handleAuthToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		DeviceCode string `json:"device_code"`
		GrantType  string `json:"grant_type"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.DeviceCode == "" {
		body.DeviceCode = r.FormValue("device_code")
	}
	acc, token, err := s.store.ConsumeDeviceCode(body.DeviceCode)
	if errors.Is(err, store.ErrDevicePending) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "authorization_pending"})
		return
	}
	if errors.Is(err, store.ErrDeviceExpired) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expired_token"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "access_denied"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": token,
		"token_type":   "Bearer",
		"account":      map[string]string{"id": acc.ID, "login": acc.Login},
	})
}

func (s *Server) handleRegisterHost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	acc, err := s.accountFromBearer(r)
	if err != nil {
		http.Error(w, "未登录", http.StatusUnauthorized)
		return
	}
	var body struct {
		RoutingID  string `json:"routing_id"`
		Name       string `json:"name"`
		Ed25519Pub string `json:"ed25519_pub"`
		BundleID   string `json:"bundle_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "请求无法解析", http.StatusBadRequest)
		return
	}
	pub, err := hex.DecodeString(strings.TrimSpace(body.Ed25519Pub))
	if err != nil || len(pub) != 32 {
		http.Error(w, "公钥不合法", http.StatusBadRequest)
		return
	}
	q := s.store.QuotaFor(acc.ID)
	if q.MaxHosts > 0 && q.HostsUsed >= q.MaxHosts {
		if _, ok := s.store.Lookup(body.RoutingID); !ok {
			http.Error(w, "超出开发机配额", http.StatusTooManyRequests)
			return
		}
	}
	host, err := s.store.RegisterHost(acc.ID, body.RoutingID, sanitizeName(body.Name), pub, body.BundleID)
	if errors.Is(err, store.ErrAccountMismatch) {
		http.Error(w, "这台开发机属于另一个账号", http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, "登记失败", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"host": map[string]string{
			"id":   host.ID,
			"name": host.Name,
		},
		"quota": q,
	})
}

func (s *Server) accountFromBearer(r *http.Request) (*store.Account, error) {
	token := strings.TrimPrefix(strings.TrimSpace(r.Header.Get("Authorization")), "Bearer ")
	if token == "" {
		return nil, store.ErrUnauthorized
	}
	return s.store.AccountByToken(token)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.opts.GitHubID == "" {
		web.Render(w, "error.html", map[string]string{"Message": "这个中继没有配置 GitHub OAuth。"})
		return
	}
	state := newHex(16)
	http.SetCookie(w, &http.Cookie{Name: "corral_oauth_state", Value: state, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
	u := url.URL{Scheme: "https", Host: "github.com", Path: "/login/oauth/authorize"}
	q := u.Query()
	q.Set("client_id", s.opts.GitHubID)
	q.Set("state", state)
	q.Set("scope", "read:user")
	q.Set("redirect_uri", strings.TrimRight(s.opts.PublicURL, "/")+"/oauth/github/callback")
	u.RawQuery = q.Encode()
	web.Render(w, "login.html", map[string]string{"AuthURL": u.String()})
}

func (s *Server) handleGitHubCallback(w http.ResponseWriter, r *http.Request) {
	stateCookie, _ := r.Cookie("corral_oauth_state")
	if stateCookie == nil || stateCookie.Value == "" || r.FormValue("state") != stateCookie.Value {
		web.Render(w, "error.html", map[string]string{"Message": "OAuth state 对不上，请重试。"})
		return
	}
	code := r.FormValue("code")
	if code == "" {
		web.Render(w, "error.html", map[string]string{"Message": "GitHub 没有返回授权码。"})
		return
	}
	login, githubID, err := s.exchangeGitHub(code)
	if err != nil {
		s.log.Info("GitHub 兑换失败", "error", err)
		web.Render(w, "error.html", map[string]string{"Message": "GitHub 登录失败。"})
		return
	}
	acc, err := s.store.UpsertAccount("github", githubID, login)
	if err != nil {
		web.Render(w, "error.html", map[string]string{"Message": "无法创建账号。"})
		return
	}
	s.opts.Cookies.Set(w, web.Session{AccountID: acc.ID, Login: acc.Login})
	http.Redirect(w, r, "/account", http.StatusFound)
}

func (s *Server) exchangeGitHub(code string) (login, id string, err error) {
	form := url.Values{}
	form.Set("client_id", s.opts.GitHubID)
	form.Set("client_secret", s.opts.GitHubSecret)
	form.Set("code", code)
	form.Set("redirect_uri", strings.TrimRight(s.opts.PublicURL, "/")+"/oauth/github/callback")
	req, err := http.NewRequest(http.MethodPost, "https://github.com/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("accept", "application/json")
	req.Header.Set("content-type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	var tokenBody struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&tokenBody)
	if tokenBody.AccessToken == "" {
		return "", "", fmt.Errorf("github token: %s", tokenBody.Error)
	}
	ureq, err := http.NewRequest(http.MethodGet, "https://api.github.com/user", nil)
	if err != nil {
		return "", "", err
	}
	ureq.Header.Set("authorization", "Bearer "+tokenBody.AccessToken)
	ureq.Header.Set("accept", "application/vnd.github+json")
	uresp, err := http.DefaultClient.Do(ureq)
	if err != nil {
		return "", "", err
	}
	defer uresp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(uresp.Body, 1<<20))
	var user struct {
		Login string `json:"login"`
		ID    int64  `json:"id"`
	}
	if err := json.Unmarshal(raw, &user); err != nil || user.Login == "" {
		return "", "", fmt.Errorf("github user")
	}
	return user.Login, fmt.Sprintf("%d", user.ID), nil
}

func (s *Server) requireSession(w http.ResponseWriter, r *http.Request) (web.Session, bool) {
	sess, ok := s.opts.Cookies.Get(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusFound)
		return web.Session{}, false
	}
	return sess, true
}

func (s *Server) handleDevicePage(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	if r.Method == http.MethodPost {
		code := strings.ToUpper(strings.ReplaceAll(r.FormValue("user_code"), "-", ""))
		if len(code) == 8 {
			code = code[:4] + "-" + code[4:]
		}
		err := s.store.ApproveDeviceCode(code, sess.AccountID)
		if err != nil {
			web.Render(w, "device.html", map[string]any{"Login": sess.Login, "Error": "设备码无效或已过期", "UserCode": r.FormValue("user_code")})
			return
		}
		web.Render(w, "device.html", map[string]any{"Login": sess.Login, "OK": true})
		return
	}
	web.Render(w, "device.html", map[string]any{"Login": sess.Login, "UserCode": r.URL.Query().Get("user_code")})
}

func (s *Server) handleAccountPage(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	hosts, _ := s.store.ListHosts(sess.AccountID)
	q := s.store.QuotaFor(sess.AccountID)
	web.Render(w, "account.html", map[string]any{
		"Login":     sess.Login,
		"Hosts":     hosts,
		"Bytes":     q.BytesUsed,
		"ByteLimit": q.MonthlyBytes,
		"Pushes":    q.PushesToday,
		"PushLimit": q.DailyPushes,
		"HostsUsed": q.HostsUsed,
		"HostLimit": q.MaxHosts,
	})
}

func newHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func newUserCode() string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	out := make([]byte, 8)
	for i := range out {
		out[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(out[:4]) + "-" + string(out[4:])
}
