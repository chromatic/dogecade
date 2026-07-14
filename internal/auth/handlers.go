package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/chromatic/dogecade/internal/services"
)

const (
	stateCookieName    = "dogecade_oidc_state"
	nonceCookieName    = "dogecade_oidc_nonce"
	redirectCookieName = "dogecade_oidc_redirect"
	oidcFlowTTL        = 10 * time.Minute
)

// Handlers wires the OIDC login/callback/logout HTTP endpoints and the
// RequireAuth/RequireAdmin middleware. exchanger is nil when OIDC isn't
// configured (DOGECADE_OIDC_* env vars unset): login then returns a clear
// 503 instead of a nil-pointer panic, matching the rest of the codebase's
// "unconfigured is a first-class state" convention.
type Handlers struct {
	sessions      *SessionManager
	exchanger     exchanger
	users         UsersLookup
	adminSubjects []AdminSubject
	secureCookies bool
}

// UsersLookup is the subset of services.UsersService that auth needs,
// narrowed to keep this package's dependency on services minimal and easy
// to fake in tests.
type UsersLookup interface {
	GetOrCreateBySubjectHash(ctx context.Context, subjectHash, displayName string, isAdmin bool) (services.User, error)
}

// NewHandlers creates a Handlers. provider may be nil if OIDC isn't
// configured.
func NewHandlers(sessions *SessionManager, provider *Provider, users UsersLookup, adminSubjects []AdminSubject, secureCookies bool) *Handlers {
	h := &Handlers{
		sessions:      sessions,
		users:         users,
		adminSubjects: adminSubjects,
		secureCookies: secureCookies,
	}
	if provider != nil {
		h.exchanger = provider
	}
	return h
}

// Configured reports whether OIDC login is available.
func (h *Handlers) Configured() bool {
	return h.exchanger != nil
}

// Login starts the OIDC flow: generates state/nonce, stashes them (plus the
// post-login redirect target) in short-lived cookies, and redirects to the
// provider's login page.
func (h *Handlers) Login(w http.ResponseWriter, r *http.Request) {
	if h.exchanger == nil {
		http.Error(w, "sign-in is not configured", http.StatusServiceUnavailable)
		return
	}

	state, err := randomToken()
	if err != nil {
		http.Error(w, "failed to start login", http.StatusInternalServerError)
		return
	}
	nonce, err := randomToken()
	if err != nil {
		http.Error(w, "failed to start login", http.StatusInternalServerError)
		return
	}

	redirectTo := r.URL.Query().Get("redirect")
	if redirectTo == "" || redirectTo[0] != '/' {
		redirectTo = "/"
	}

	h.setFlowCookie(w, stateCookieName, state)
	h.setFlowCookie(w, nonceCookieName, nonce)
	h.setFlowCookie(w, redirectCookieName, redirectTo)

	http.Redirect(w, r, h.exchanger.AuthCodeURL(state, nonce), http.StatusFound)
}

// Callback completes the OIDC flow: validates state, exchanges the code,
// verifies the ID token, resolves (or creates) the local user account, and
// issues a session cookie.
func (h *Handlers) Callback(w http.ResponseWriter, r *http.Request) {
	if h.exchanger == nil {
		http.Error(w, "sign-in is not configured", http.StatusServiceUnavailable)
		return
	}

	stateCookie, err := r.Cookie(stateCookieName)
	if err != nil || stateCookie.Value == "" || stateCookie.Value != r.URL.Query().Get("state") {
		http.Error(w, "invalid login state", http.StatusBadRequest)
		return
	}
	nonceCookie, err := r.Cookie(nonceCookieName)
	if err != nil || nonceCookie.Value == "" {
		http.Error(w, "invalid login state", http.StatusBadRequest)
		return
	}
	redirectTo := "/"
	if rc, err := r.Cookie(redirectCookieName); err == nil && rc.Value != "" {
		redirectTo = rc.Value
	}
	h.clearFlowCookies(w)

	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing authorization code", http.StatusBadRequest)
		return
	}

	claims, err := h.exchanger.Exchange(r.Context(), code, nonceCookie.Value)
	if err != nil {
		http.Error(w, "login failed: "+err.Error(), http.StatusBadGateway)
		return
	}

	subjectHash := SubjectHash(claims.Issuer, claims.Subject)
	displayName := claims.Name
	if displayName == "" {
		displayName = claims.Email
	}
	isAdmin := IsAdminSubject(h.adminSubjects, claims.Issuer, claims.Subject)
	slog.Info("oidc login", "issuer", claims.Issuer, "subject", claims.Subject, "is_admin", isAdmin)

	user, err := h.users.GetOrCreateBySubjectHash(r.Context(), subjectHash, displayName, isAdmin)
	if err != nil {
		http.Error(w, "failed to resolve account", http.StatusInternalServerError)
		return
	}

	if err := h.sessions.Issue(w, user.ID, user.IsAdmin); err != nil {
		http.Error(w, "failed to start session", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, redirectTo, http.StatusFound)
}

// Logout clears the session cookie and redirects to "/".
func (h *Handlers) Logout(w http.ResponseWriter, r *http.Request) {
	h.sessions.Clear(w)
	http.Redirect(w, r, "/", http.StatusFound)
}

// SubjectHash returns sha256(issuer||subject) as a hex string, the stable
// per-identity key used as users.subject_hash so we never store a raw OIDC
// subject.
func SubjectHash(issuer, subject string) string {
	sum := sha256.Sum256([]byte(issuer + "|" + subject))
	return hex.EncodeToString(sum[:])
}

func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func (h *Handlers) setFlowCookie(w http.ResponseWriter, name, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.secureCookies,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(oidcFlowTTL),
	})
}

func (h *Handlers) clearFlowCookies(w http.ResponseWriter) {
	for _, name := range []string{stateCookieName, nonceCookieName, redirectCookieName} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			HttpOnly: true,
			Secure:   h.secureCookies,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   -1,
		})
	}
}

type contextKey int

const sessionContextKey contextKey = iota

// ErrNoSession is returned by CurrentSession when the request context has
// no attached session (RequireAuth/OptionalAuth wasn't applied, or the
// visitor isn't signed in).
var ErrNoSession = errors.New("no session in request context")

// OptionalAuth attaches the caller's session to the request context if
// present and valid, but lets the request through either way. Handlers that
// render differently for signed-in vs. anonymous visitors (e.g. the home
// page) should use this instead of RequireAuth.
func (h *Handlers) OptionalAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sess, err := h.sessions.FromRequest(r); err == nil {
			r = r.WithContext(context.WithValue(r.Context(), sessionContextKey, sess))
		}
		next.ServeHTTP(w, r)
	})
}

// RequireAuth redirects to the login page (preserving the original path as
// the post-login redirect target) unless the request carries a valid
// session.
func (h *Handlers) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, err := h.sessions.FromRequest(r)
		if err != nil {
			redirectURL := "/auth/login?redirect=" + url.QueryEscape(r.URL.Path)
			http.Redirect(w, r, redirectURL, http.StatusFound)
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), sessionContextKey, sess))
		next.ServeHTTP(w, r)
	})
}

// RequireAdmin is like RequireAuth but also requires the session's admin
// flag; non-admins get a 403 rather than being redirected to login (they
// are logged in, just not authorized).
func (h *Handlers) RequireAdmin(next http.Handler) http.Handler {
	return h.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, _ := CurrentSession(r)
		if !sess.IsAdmin {
			http.Error(w, "admin access required", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	}))
}

// CurrentSession retrieves the session attached to r by OptionalAuth or
// RequireAuth, if any.
func CurrentSession(r *http.Request) (Session, error) {
	sess, ok := r.Context().Value(sessionContextKey).(Session)
	if !ok {
		return Session{}, ErrNoSession
	}
	return sess, nil
}
