package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSessionRoundTrip(t *testing.T) {
	m := NewSessionManager([]byte("test-secret"), false)

	rec := httptest.NewRecorder()
	if err := m.Issue(rec, 42, true); err != nil {
		t.Fatalf("Issue failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}

	sess, err := m.FromRequest(req)
	if err != nil {
		t.Fatalf("FromRequest failed: %v", err)
	}
	if sess.UserID != 42 || !sess.IsAdmin {
		t.Errorf("expected UserID=42 IsAdmin=true, got %+v", sess)
	}
}

func TestSessionRejectsTamperedCookie(t *testing.T) {
	m := NewSessionManager([]byte("test-secret"), false)

	rec := httptest.NewRecorder()
	if err := m.Issue(rec, 1, false); err != nil {
		t.Fatalf("Issue failed: %v", err)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(cookies))
	}
	tampered := cookies[0]
	tampered.Value = tampered.Value + "x"

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(tampered)

	if _, err := m.FromRequest(req); err != ErrInvalidSession {
		t.Errorf("expected ErrInvalidSession for tampered cookie, got %v", err)
	}
}

func TestSessionRejectsWrongSecret(t *testing.T) {
	issuer := NewSessionManager([]byte("secret-a"), false)
	verifier := NewSessionManager([]byte("secret-b"), false)

	rec := httptest.NewRecorder()
	if err := issuer.Issue(rec, 1, false); err != nil {
		t.Fatalf("Issue failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}

	if _, err := verifier.FromRequest(req); err != ErrInvalidSession {
		t.Errorf("expected ErrInvalidSession for wrong secret, got %v", err)
	}
}

func TestSessionRejectsMissingCookie(t *testing.T) {
	m := NewSessionManager([]byte("test-secret"), false)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if _, err := m.FromRequest(req); err != ErrInvalidSession {
		t.Errorf("expected ErrInvalidSession for missing cookie, got %v", err)
	}
}

func TestSessionRejectsExpired(t *testing.T) {
	m := NewSessionManager([]byte("test-secret"), false)
	sess := Session{UserID: 1, ExpiresAt: time.Now().Add(-time.Minute)}
	token, err := m.encode(sess)
	if err != nil {
		t.Fatalf("encode failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: token})

	if _, err := m.FromRequest(req); err != ErrInvalidSession {
		t.Errorf("expected ErrInvalidSession for expired session, got %v", err)
	}
}

func TestIssueSetsSecureFlagFromConstructor(t *testing.T) {
	m := NewSessionManager([]byte("test-secret"), true)
	rec := httptest.NewRecorder()
	if err := m.Issue(rec, 1, false); err != nil {
		t.Fatalf("Issue failed: %v", err)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure {
		t.Errorf("expected Secure cookie flag to be set")
	}
}
