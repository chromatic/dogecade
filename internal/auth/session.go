package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// SessionCookieName is the cookie carrying a customer's signed session.
const SessionCookieName = "dogecade_session"

// SessionTTL is how long a session cookie remains valid after issuance.
const SessionTTL = 30 * 24 * time.Hour

// ErrInvalidSession is returned when a session cookie is missing, malformed,
// signed with a different secret, or expired.
var ErrInvalidSession = errors.New("invalid session")

// Session is the payload carried inside a signed session cookie.
type Session struct {
	UserID    int64     `json:"user_id"`
	IsAdmin   bool      `json:"is_admin"`
	ExpiresAt time.Time `json:"expires_at"`
}

// SessionManager signs and verifies session cookies with HMAC-SHA256 over a
// server-held secret. The cookie value is "<base64url payload>.<base64url
// signature>"; there's nothing for the client to read or tamper with since
// the payload only holds a user ID and admin flag, not credentials.
type SessionManager struct {
	secret        []byte
	secureCookies bool
}

// NewSessionManager creates a SessionManager. secureCookies should be true
// whenever the service is reachable over https (sets the cookie's Secure
// flag), matching the base URL scheme.
func NewSessionManager(secret []byte, secureCookies bool) *SessionManager {
	return &SessionManager{secret: secret, secureCookies: secureCookies}
}

// Issue signs a session for userID/isAdmin and sets it as an HttpOnly,
// SameSite=Lax cookie on w.
func (m *SessionManager) Issue(w http.ResponseWriter, userID int64, isAdmin bool) error {
	sess := Session{UserID: userID, IsAdmin: isAdmin, ExpiresAt: time.Now().Add(SessionTTL)}
	token, err := m.encode(sess)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   m.secureCookies,
		SameSite: http.SameSiteLaxMode,
		Expires:  sess.ExpiresAt,
	})
	return nil
}

// Clear expires the session cookie on w, logging the user out.
func (m *SessionManager) Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   m.secureCookies,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// FromRequest reads and verifies the session cookie on r, returning
// ErrInvalidSession if it's missing, malformed, or expired.
func (m *SessionManager) FromRequest(r *http.Request) (Session, error) {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil {
		return Session{}, ErrInvalidSession
	}
	sess, err := m.decode(cookie.Value)
	if err != nil {
		return Session{}, err
	}
	if time.Now().After(sess.ExpiresAt) {
		return Session{}, ErrInvalidSession
	}
	return sess, nil
}

func (m *SessionManager) encode(sess Session) (string, error) {
	payload, err := json.Marshal(sess)
	if err != nil {
		return "", fmt.Errorf("failed to marshal session: %w", err)
	}
	payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
	sig := m.sign(payloadB64)
	return payloadB64 + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func (m *SessionManager) decode(token string) (Session, error) {
	dot := -1
	for i := len(token) - 1; i >= 0; i-- {
		if token[i] == '.' {
			dot = i
			break
		}
	}
	if dot < 0 {
		return Session{}, ErrInvalidSession
	}
	payloadB64, sigB64 := token[:dot], token[dot+1:]

	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return Session{}, ErrInvalidSession
	}
	expectedSig := m.sign(payloadB64)
	if subtle.ConstantTimeCompare(sig, expectedSig) != 1 {
		return Session{}, ErrInvalidSession
	}

	payload, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return Session{}, ErrInvalidSession
	}
	var sess Session
	if err := json.Unmarshal(payload, &sess); err != nil {
		return Session{}, ErrInvalidSession
	}
	return sess, nil
}

func (m *SessionManager) sign(payloadB64 string) []byte {
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte(payloadB64))
	return mac.Sum(nil)
}
