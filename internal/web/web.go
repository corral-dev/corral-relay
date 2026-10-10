package web

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/json"
	"html/template"
	"net/http"
	"time"
)

//go:embed templates/*.html
var templateFS embed.FS

var pages = template.Must(template.ParseFS(templateFS, "templates/*.html"))

type Session struct {
	AccountID string `json:"account_id"`
	Login     string `json:"login"`
}

type CookieJar struct {
	secret []byte
}

func NewCookieJar(secret []byte) *CookieJar {
	if len(secret) == 0 {
		secret = make([]byte, 32)
		_, _ = rand.Read(secret)
	}
	return &CookieJar{secret: secret}
}

func (c *CookieJar) Set(w http.ResponseWriter, s Session) {
	raw, _ := json.Marshal(s)
	mac := hmac.New(sha256.New, c.secret)
	mac.Write(raw)
	token := base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	http.SetCookie(w, &http.Cookie{
		Name:     "corral_session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(30 * 24 * time.Hour),
	})
}

func (c *CookieJar) Get(r *http.Request) (Session, bool) {
	cookie, err := r.Cookie("corral_session")
	if err != nil {
		return Session{}, false
	}
	parts := split2(cookie.Value, '.')
	if len(parts) != 2 {
		return Session{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Session{}, false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Session{}, false
	}
	mac := hmac.New(sha256.New, c.secret)
	mac.Write(raw)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return Session{}, false
	}
	var s Session
	if json.Unmarshal(raw, &s) != nil || s.AccountID == "" {
		return Session{}, false
	}
	return s, true
}

func split2(s string, sep byte) []string {
	i := -1
	for idx := 0; idx < len(s); idx++ {
		if s[idx] == sep {
			i = idx
		}
	}
	if i < 0 {
		return nil
	}
	return []string{s[:i], s[i+1:]}
}

func Render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("content-type", "text/html; charset=utf-8")
	if err := pages.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, "页面渲染失败", http.StatusInternalServerError)
	}
}
