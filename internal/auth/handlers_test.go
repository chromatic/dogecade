package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/chromatic/dogecade/internal/services"
)

type fakeExchanger struct {
	authURL string
	claims  IDClaims
	err     error
}

func (f *fakeExchanger) AuthCodeURL(state, nonce string) string {
	return f.authURL + "?state=" + state + "&nonce=" + nonce
}

func (f *fakeExchanger) Exchange(ctx context.Context, code, nonce string) (IDClaims, error) {
	if f.err != nil {
		return IDClaims{}, f.err
	}
	return f.claims, nil
}

type fakeUsersLookup struct {
	users       map[string]services.User
	nextID      int64
	lastIsAdmin bool
	lastSubject string
	lastDisplay string
}

func newFakeUsersLookup() *fakeUsersLookup {
	return &fakeUsersLookup{users: make(map[string]services.User), nextID: 1}
}

func (f *fakeUsersLookup) GetOrCreateBySubjectHash(ctx context.Context, subjectHash, displayName string, isAdmin bool) (services.User, error) {
	f.lastSubject = subjectHash
	f.lastDisplay = displayName
	f.lastIsAdmin = isAdmin
	if u, ok := f.users[subjectHash]; ok {
		return u, nil
	}
	u := services.User{ID: f.nextID, DisplayName: displayName, IsAdmin: isAdmin}
	f.nextID++
	f.users[subjectHash] = u
	return u, nil
}

func newTestHandlers(ex exchanger, users UsersLookup, adminSubjects []AdminSubject) *Handlers {
	h := &Handlers{
		sessions:      NewSessionManager([]byte("test-secret"), false),
		exchanger:     ex,
		users:         users,
		adminSubjects: adminSubjects,
	}
	return h
}

func TestLoginRedirectsToProviderAndSetsFlowCookies(t *testing.T) {
	h := newTestHandlers(&fakeExchanger{authURL: "https://issuer.example/auth"}, newFakeUsersLookup(), nil)

	req := httptest.NewRequest(http.MethodGet, "/auth/login?redirect=/buy", nil)
	rec := httptest.NewRecorder()
	h.Login(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if loc == "" {
		t.Fatal("expected Location header")
	}

	var sawState, sawNonce, sawRedirect bool
	for _, c := range rec.Result().Cookies() {
		switch c.Name {
		case stateCookieName:
			sawState = c.Value != ""
		case nonceCookieName:
			sawNonce = c.Value != ""
		case redirectCookieName:
			sawRedirect = c.Value == "/buy"
		}
	}
	if !sawState || !sawNonce || !sawRedirect {
		t.Errorf("expected state/nonce/redirect flow cookies to be set, got %+v", rec.Result().Cookies())
	}
}

func TestLoginWithoutProviderReturns503(t *testing.T) {
	h := newTestHandlers(nil, newFakeUsersLookup(), nil)
	req := httptest.NewRequest(http.MethodGet, "/auth/login", nil)
	rec := httptest.NewRecorder()
	h.Login(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func TestCallbackSucceedsAndIssuesSession(t *testing.T) {
	users := newFakeUsersLookup()
	h := newTestHandlers(&fakeExchanger{claims: IDClaims{Issuer: "iss", Subject: "sub-1", Name: "Alice"}}, users, nil)

	// Simulate a completed Login round-trip: capture the flow cookies.
	loginReq := httptest.NewRequest(http.MethodGet, "/auth/login", nil)
	loginRec := httptest.NewRecorder()
	h.Login(loginRec, loginReq)
	flowCookies := loginRec.Result().Cookies()

	var state, nonce string
	for _, c := range flowCookies {
		if c.Name == stateCookieName {
			state = c.Value
		}
		if c.Name == nonceCookieName {
			nonce = c.Value
		}
	}

	cbReq := httptest.NewRequest(http.MethodGet, "/auth/callback?state="+state+"&code=abc123", nil)
	for _, c := range flowCookies {
		cbReq.AddCookie(c)
	}
	cbRec := httptest.NewRecorder()
	h.Callback(cbRec, cbReq)

	if cbRec.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d body=%s", cbRec.Code, cbRec.Body.String())
	}
	if cbRec.Header().Get("Location") != "/" {
		t.Errorf("expected redirect to /, got %q", cbRec.Header().Get("Location"))
	}

	var gotSession bool
	for _, c := range cbRec.Result().Cookies() {
		if c.Name == SessionCookieName && c.Value != "" {
			gotSession = true
		}
	}
	if !gotSession {
		t.Error("expected a session cookie to be issued")
	}
	if users.lastDisplay != "Alice" {
		t.Errorf("expected display name Alice, got %q", users.lastDisplay)
	}
	expectedHash := SubjectHash("iss", "sub-1")
	if users.lastSubject != expectedHash {
		t.Errorf("expected subject hash %q, got %q", expectedHash, users.lastSubject)
	}
	_ = nonce
}

func TestCallbackBootstrapsAdmin(t *testing.T) {
	users := newFakeUsersLookup()
	admins := []AdminSubject{{Issuer: "iss", Subject: "admin-sub"}}
	h := newTestHandlers(&fakeExchanger{claims: IDClaims{Issuer: "iss", Subject: "admin-sub", Name: "Boss"}}, users, admins)

	loginRec := httptest.NewRecorder()
	h.Login(loginRec, httptest.NewRequest(http.MethodGet, "/auth/login", nil))
	flowCookies := loginRec.Result().Cookies()
	var state string
	for _, c := range flowCookies {
		if c.Name == stateCookieName {
			state = c.Value
		}
	}

	cbReq := httptest.NewRequest(http.MethodGet, "/auth/callback?state="+state+"&code=abc", nil)
	for _, c := range flowCookies {
		cbReq.AddCookie(c)
	}
	cbRec := httptest.NewRecorder()
	h.Callback(cbRec, cbReq)

	if !users.lastIsAdmin {
		t.Error("expected admin subject to bootstrap as admin")
	}
}

func TestCallbackRejectsStateMismatch(t *testing.T) {
	h := newTestHandlers(&fakeExchanger{claims: IDClaims{Issuer: "iss", Subject: "sub"}}, newFakeUsersLookup(), nil)

	loginRec := httptest.NewRecorder()
	h.Login(loginRec, httptest.NewRequest(http.MethodGet, "/auth/login", nil))
	flowCookies := loginRec.Result().Cookies()

	cbReq := httptest.NewRequest(http.MethodGet, "/auth/callback?state=wrong&code=abc", nil)
	for _, c := range flowCookies {
		cbReq.AddCookie(c)
	}
	cbRec := httptest.NewRecorder()
	h.Callback(cbRec, cbReq)

	if cbRec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for state mismatch, got %d", cbRec.Code)
	}
}

func TestLogoutClearsSessionCookie(t *testing.T) {
	h := newTestHandlers(nil, newFakeUsersLookup(), nil)
	rec := httptest.NewRecorder()
	h.Logout(rec, httptest.NewRequest(http.MethodGet, "/auth/logout", nil))

	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d", rec.Code)
	}
	var cleared bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookieName && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("expected session cookie to be cleared")
	}
}

func TestRequireAuthRedirectsWhenNoSession(t *testing.T) {
	h := newTestHandlers(nil, newFakeUsersLookup(), nil)
	called := false
	protected := h.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodGet, "/history", nil)
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)

	if called {
		t.Error("expected inner handler not to be called without a session")
	}
	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if loc != "/auth/login?redirect=%2Fhistory" {
		t.Errorf("expected redirect to login with redirect target, got %q", loc)
	}
}

func TestRequireAuthAllowsValidSession(t *testing.T) {
	h := newTestHandlers(nil, newFakeUsersLookup(), nil)

	rec := httptest.NewRecorder()
	if err := h.sessions.Issue(rec, 7, false); err != nil {
		t.Fatalf("Issue failed: %v", err)
	}

	var gotUserID int64
	protected := h.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, err := CurrentSession(r)
		if err != nil {
			t.Fatalf("expected session in context, got err %v", err)
		}
		gotUserID = sess.UserID
	}))

	req := httptest.NewRequest(http.MethodGet, "/history", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	protected.ServeHTTP(httptest.NewRecorder(), req)

	if gotUserID != 7 {
		t.Errorf("expected UserID=7, got %d", gotUserID)
	}
}

func TestRequireAdminRejectsNonAdmin(t *testing.T) {
	h := newTestHandlers(nil, newFakeUsersLookup(), nil)
	rec := httptest.NewRecorder()
	if err := h.sessions.Issue(rec, 1, false); err != nil {
		t.Fatalf("Issue failed: %v", err)
	}

	protected := h.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("inner handler should not be called for non-admin")
	}))

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	resultRec := httptest.NewRecorder()
	protected.ServeHTTP(resultRec, req)

	if resultRec.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resultRec.Code)
	}
}

func TestRequireAdminAllowsAdmin(t *testing.T) {
	h := newTestHandlers(nil, newFakeUsersLookup(), nil)
	rec := httptest.NewRecorder()
	if err := h.sessions.Issue(rec, 1, true); err != nil {
		t.Fatalf("Issue failed: %v", err)
	}

	called := false
	protected := h.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	protected.ServeHTTP(httptest.NewRecorder(), req)

	if !called {
		t.Error("expected inner handler to be called for admin")
	}
}

func TestOptionalAuthAttachesSessionWhenPresent(t *testing.T) {
	h := newTestHandlers(nil, newFakeUsersLookup(), nil)
	rec := httptest.NewRecorder()
	if err := h.sessions.Issue(rec, 9, false); err != nil {
		t.Fatalf("Issue failed: %v", err)
	}

	var found bool
	wrapped := h.OptionalAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := CurrentSession(r)
		found = err == nil
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	wrapped.ServeHTTP(httptest.NewRecorder(), req)
	if !found {
		t.Error("expected session to be attached")
	}
}

func TestOptionalAuthPassesThroughWithoutSession(t *testing.T) {
	h := newTestHandlers(nil, newFakeUsersLookup(), nil)
	called := false
	wrapped := h.OptionalAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if _, err := CurrentSession(r); err != ErrNoSession {
			t.Errorf("expected ErrNoSession, got %v", err)
		}
	}))
	wrapped.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !called {
		t.Error("expected inner handler to be called even without a session")
	}
}
