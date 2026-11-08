package server

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	cookieName  = "ms_session"
	sessionLife = 7 * 24 * time.Hour
	pbkdf2Iter  = 120000 // 单次登录可接受，暴力破解成本高
)

type Auth struct {
	apiKey string
	secret []byte

	mu       sync.RWMutex
	passHash string // "" 表示还没设密码
}

func NewAuth(apiKey, passHash string, secret []byte) *Auth {
	return &Auth{apiKey: apiKey, passHash: passHash, secret: secret}
}

// PassHash 返回当前密码哈希（只读，给持久化用）。
func (a *Auth) PassHash() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.passHash
}

// SetPassHash 在内存里换掉密码哈希；磁盘持久化由调用方负责。
func (a *Auth) SetPassHash(h string) {
	a.mu.Lock()
	a.passHash = h
	a.mu.Unlock()
}

// CheckPassword 校验明文密码，未设置密码时一律失败。
func (a *Auth) CheckPassword(pw string) bool {
	a.mu.RLock()
	h := a.passHash
	a.mu.RUnlock()
	return h != "" && verifyPassword(pw, h)
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return hex.EncodeToString([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
	}
	return hex.EncodeToString(b)
}

func HashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, pw, salt, pbkdf2Iter, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2$%d$%s$%s", pbkdf2Iter, base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

func verifyPassword(pw, stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 1000 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, pw, salt, iter, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

func (a *Auth) sign(exp string) string {
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte(exp))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (a *Auth) issueSession() string {
	exp := strconv.FormatInt(time.Now().Add(sessionLife).Unix(), 10)
	return exp + "." + a.sign(exp)
}

func (a *Auth) validSession(tok string) bool {
	i := strings.IndexByte(tok, '.')
	if i <= 0 {
		return false
	}
	exp, sig := tok[:i], tok[i+1:]
	if subtle.ConstantTimeCompare([]byte(sig), []byte(a.sign(exp))) != 1 {
		return false
	}
	n, err := strconv.ParseInt(exp, 10, 64)
	if err != nil {
		return false
	}
	return time.Now().Unix() < n
}

func (a *Auth) checkAPIKey(k string) bool {
	if a.apiKey == "" || k == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(k), []byte(a.apiKey)) == 1
}

// authorized 接受三种凭证：Cookie 会话、Bearer API Key、?token= API Key。
// 最后一种是为了 <video> / EventSource 这类不能自定义请求头的场景。
func (a *Auth) authorized(r *http.Request) bool {
	if c, err := r.Cookie(cookieName); err == nil && a.validSession(c.Value) {
		return true
	}
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") && a.checkAPIKey(strings.TrimSpace(h[7:])) {
		return true
	}
	if k := r.Header.Get("X-API-Key"); a.checkAPIKey(k) {
		return true
	}
	if t := r.URL.Query().Get("token"); t != "" {
		if a.checkAPIKey(t) {
			return true
		}
		if a.validSession(t) {
			return true
		}
	}
	return false
}

func (a *Auth) setCookie(w http.ResponseWriter, tok string) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionLife.Seconds()),
	})
}

func (a *Auth) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
}
