package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chromatic/dogecade/internal/auth"
	"github.com/chromatic/dogecade/internal/services"
	"github.com/chromatic/dogecade/internal/store"
)

func seedMachine(t *testing.T, ctx context.Context, s *store.Store, slug string, isActive bool) int64 {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	activeInt := 0
	if isActive {
		activeInt = 1
	}
	var id int64
	err := s.DB().QueryRowContext(ctx,
		"INSERT INTO machines (slug, name, is_active, created_at) VALUES (?, ?, ?, ?) RETURNING id",
		slug, slug, activeInt, now,
	).Scan(&id)
	if err != nil {
		t.Fatalf("failed to seed machine: %v", err)
	}
	return id
}

func seedPoolAddress(t *testing.T, ctx context.Context, s *store.Store, addr string, purpose string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	var batchID int64
	err := s.DB().QueryRowContext(ctx,
		"INSERT INTO address_batches (source_note, address_count, loaded_at) VALUES (?, ?, ?) RETURNING id",
		"test batch", 1, now,
	).Scan(&batchID)
	if err != nil {
		t.Fatalf("failed to insert batch: %v", err)
	}
	if _, err := s.DB().ExecContext(ctx,
		"INSERT INTO addresses (address, batch_id, state, purpose) VALUES (?, ?, 'pool', ?)",
		addr, batchID, purpose,
	); err != nil {
		t.Fatalf("failed to seed pool address: %v", err)
	}
}

type testServerBundle struct {
	server   *Server
	sessions *auth.SessionManager
	users    *services.UsersService
	ledger   *services.LedgerService
	purchase *services.PurchaseService
}

func newTestServerBundle(t *testing.T, checker nodeHealthChecker) (*store.Store, testServerBundle) {
	t.Helper()
	s := openTestStore(t)

	users := services.NewUsersService(s)
	ledger := services.NewLedgerService(s)
	settings := services.NewSettingsService(s)
	purchase := services.NewPurchaseService(s)
	redemption := services.NewRedemptionService(s, ledger)
	machines := services.NewMachinesService(s)
	directPay := services.NewDirectPayService(s, settings)

	sessions := auth.NewSessionManager([]byte("test-secret"), false)
	authHandlers := auth.NewHandlers(sessions, nil, users, nil, false)

	server := NewServer(s, nil, checker)
	if err := server.RegisterCustomerRoutes(CustomerDeps{
		Auth:       authHandlers,
		Ledger:     ledger,
		Purchase:   purchase,
		Redemption: redemption,
		Machines:   machines,
		Settings:   settings,
		DirectPay:  directPay,
	}); err != nil {
		t.Fatalf("RegisterCustomerRoutes failed: %v", err)
	}

	return s, testServerBundle{server: server, sessions: sessions, users: users, ledger: ledger, purchase: purchase}
}

func sessionCookie(t *testing.T, sessions *auth.SessionManager, userID int64, isAdmin bool) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := sessions.Issue(rec, userID, isAdmin); err != nil {
		t.Fatalf("Issue failed: %v", err)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(cookies))
	}
	return cookies[0]
}

func TestIndexPageSignedOut(t *testing.T) {
	_, bundle := newTestServerBundle(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	bundle.server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Sign in") {
		t.Errorf("expected sign-in prompt, got body: %s", rec.Body.String())
	}
}

func TestIndexPageSignedInShowsBalance(t *testing.T) {
	s, bundle := newTestServerBundle(t, nil)
	ctx := context.Background()

	u, err := bundle.users.GetOrCreateBySubjectHash(ctx, "hash-1", "Alice", false)
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}
	if err := bundle.ledger.CreditPurchase(ctx, u.ID, seedDepositForBalance(t, ctx, s, u.ID), 3); err != nil {
		t.Fatalf("failed to credit purchase: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(sessionCookie(t, bundle.sessions, u.ID, false))
	rec := httptest.NewRecorder()
	bundle.server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), ">3<") {
		t.Errorf("expected balance of 3 tokens in body, got: %s", rec.Body.String())
	}
}

// seedDepositForBalance seeds a token_deposit address assigned to userID and
// a 'credited' deposit against it, returning the new deposit's ID. Only used
// to satisfy token_ledger.deposit_id's FOREIGN KEY when tests need to credit
// a balance directly without exercising the full deposit pipeline.
var depositSeedCounter int

func seedDepositForBalance(t *testing.T, ctx context.Context, s *store.Store, userID int64) int64 {
	t.Helper()
	depositSeedCounter++
	addr := "DBalanceSeedAddr" + string(rune('A'+depositSeedCounter))
	seedPoolAddress(t, ctx, s, addr, "token_deposit")

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := s.DB().ExecContext(ctx,
		"UPDATE addresses SET state = 'assigned', assigned_at = ?, user_id = ? WHERE address = ?",
		now, userID, addr,
	); err != nil {
		t.Fatalf("failed to assign seeded address: %v", err)
	}

	var addressID int64
	if err := s.DB().QueryRowContext(ctx, "SELECT id FROM addresses WHERE address = ?", addr).Scan(&addressID); err != nil {
		t.Fatalf("failed to look up seeded address: %v", err)
	}

	var depositID int64
	err := s.DB().QueryRowContext(ctx,
		`INSERT INTO deposits (address_id, txid, vout, amount_koinu, confirmations, state, created_at)
		 VALUES (?, ?, 0, 300000000, 1, 'credited', ?) RETURNING id`,
		addressID, "test-txid-"+addr, now,
	).Scan(&depositID)
	if err != nil {
		t.Fatalf("failed to seed deposit: %v", err)
	}
	return depositID
}

func TestMachinesListShowsActiveMachines(t *testing.T) {
	s, bundle := newTestServerBundle(t, nil)
	seedMachine(t, context.Background(), s, "lotr", true)
	seedMachine(t, context.Background(), s, "retired", false)

	req := httptest.NewRequest(http.MethodGet, "/machines", nil)
	rec := httptest.NewRecorder()
	bundle.server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/m/lotr") {
		t.Errorf("expected active machine link, got: %s", body)
	}
	if strings.Contains(body, "/m/retired") {
		t.Errorf("expected inactive machine to be excluded, got: %s", body)
	}
}

func TestMachineRedeemRequiresAuth(t *testing.T) {
	s, bundle := newTestServerBundle(t, nil)
	seedMachine(t, context.Background(), s, "lotr", true)

	req := httptest.NewRequest(http.MethodPost, "/m/lotr", nil)
	rec := httptest.NewRecorder()
	bundle.server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected redirect to login, got %d", rec.Code)
	}
}

func TestMachineRedeemInsufficientBalance(t *testing.T) {
	s, bundle := newTestServerBundle(t, nil)
	ctx := context.Background()
	seedMachine(t, ctx, s, "lotr", true)
	u, err := bundle.users.GetOrCreateBySubjectHash(ctx, "hash-2", "Bob", false)
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/m/lotr", nil)
	req.AddCookie(sessionCookie(t, bundle.sessions, u.ID, false))
	rec := httptest.NewRecorder()
	bundle.server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "have any tokens") {
		t.Errorf("expected insufficient-balance message, got: %s", rec.Body.String())
	}
}

func TestMachineRedeemSuccess(t *testing.T) {
	s, bundle := newTestServerBundle(t, nil)
	ctx := context.Background()
	seedMachine(t, ctx, s, "lotr", true)
	u, err := bundle.users.GetOrCreateBySubjectHash(ctx, "hash-3", "Carol", false)
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}
	if err := bundle.ledger.CreditPurchase(ctx, u.ID, seedDepositForBalance(t, ctx, s, u.ID), 1); err != nil {
		t.Fatalf("failed to credit purchase: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/m/lotr", nil)
	req.AddCookie(sessionCookie(t, bundle.sessions, u.ID, false))
	rec := httptest.NewRecorder()
	bundle.server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Credit sent") {
		t.Errorf("expected success message, got: %s", rec.Body.String())
	}

	balance, err := bundle.ledger.Balance(ctx, u.ID)
	if err != nil {
		t.Fatalf("Balance failed: %v", err)
	}
	if balance != 0 {
		t.Errorf("expected balance debited to 0, got %d", balance)
	}
}

func TestBuyPausedWhenNodeNotOk(t *testing.T) {
	s, bundle := newTestServerBundle(t, &fakeNodeHealthChecker{state: "syncing"})
	ctx := context.Background()
	u, err := bundle.users.GetOrCreateBySubjectHash(ctx, "hash-4", "Dave", false)
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}
	_ = s

	req := httptest.NewRequest(http.MethodGet, "/buy", nil)
	req.AddCookie(sessionCookie(t, bundle.sessions, u.ID, false))
	rec := httptest.NewRecorder()
	bundle.server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "syncing") {
		t.Errorf("expected syncing pause message, got: %s", rec.Body.String())
	}
}

func TestBuyPausedWhenPoolEmpty(t *testing.T) {
	s, bundle := newTestServerBundle(t, &fakeNodeHealthChecker{state: "ok"})
	ctx := context.Background()
	u, err := bundle.users.GetOrCreateBySubjectHash(ctx, "hash-5", "Erin", false)
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}
	_ = s

	req := httptest.NewRequest(http.MethodGet, "/buy", nil)
	req.AddCookie(sessionCookie(t, bundle.sessions, u.ID, false))
	rec := httptest.NewRecorder()
	bundle.server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "out of available deposit addresses") {
		t.Errorf("expected pool-empty pause message, got: %s", rec.Body.String())
	}
}

func TestBuyAssignsAddressAndRendersQR(t *testing.T) {
	s, bundle := newTestServerBundle(t, &fakeNodeHealthChecker{state: "ok"})
	ctx := context.Background()
	seedPoolAddress(t, ctx, s, "DTestPoolAddr1", "token_deposit")
	u, err := bundle.users.GetOrCreateBySubjectHash(ctx, "hash-6", "Frank", false)
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/buy", nil)
	req.AddCookie(sessionCookie(t, bundle.sessions, u.ID, false))
	rec := httptest.NewRecorder()
	bundle.server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "DTestPoolAddr1") {
		t.Errorf("expected assigned address in body, got: %s", body)
	}
	if !strings.Contains(body, "data:image/png;base64,") {
		t.Errorf("expected embedded QR code image, got: %s", body)
	}
}

func TestBuyStatusReportsWaitingWithoutDeposit(t *testing.T) {
	s, bundle := newTestServerBundle(t, &fakeNodeHealthChecker{state: "ok"})
	ctx := context.Background()
	seedPoolAddress(t, ctx, s, "DTestPoolAddr2", "token_deposit")
	u, err := bundle.users.GetOrCreateBySubjectHash(ctx, "hash-status", "Grant", false)
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}
	address, err := bundle.purchase.StartPurchase(ctx, u.ID)
	if err != nil {
		t.Fatalf("failed to start purchase: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/buy/status?address="+address, nil)
	req.AddCookie(sessionCookie(t, bundle.sessions, u.ID, false))
	rec := httptest.NewRecorder()
	bundle.server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"state":"waiting"`) {
		t.Errorf("expected waiting state, got: %s", rec.Body.String())
	}
}

func TestBuyStatusRejectsAddressNotOwnedByCaller(t *testing.T) {
	s, bundle := newTestServerBundle(t, &fakeNodeHealthChecker{state: "ok"})
	ctx := context.Background()
	seedPoolAddress(t, ctx, s, "DTestPoolAddr3", "token_deposit")
	owner, err := bundle.users.GetOrCreateBySubjectHash(ctx, "hash-owner", "Owner", false)
	if err != nil {
		t.Fatalf("failed to create owner: %v", err)
	}
	address, err := bundle.purchase.StartPurchase(ctx, owner.ID)
	if err != nil {
		t.Fatalf("failed to start purchase: %v", err)
	}
	other, err := bundle.users.GetOrCreateBySubjectHash(ctx, "hash-other", "Other", false)
	if err != nil {
		t.Fatalf("failed to create other user: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/buy/status?address="+address, nil)
	req.AddCookie(sessionCookie(t, bundle.sessions, other.ID, false))
	rec := httptest.NewRecorder()
	bundle.server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for address owned by another user, got %d", rec.Code)
	}
}

func TestHistoryPageRequiresAuthAndShowsEntries(t *testing.T) {
	s, bundle := newTestServerBundle(t, nil)
	ctx := context.Background()
	u, err := bundle.users.GetOrCreateBySubjectHash(ctx, "hash-7", "Grace", false)
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}
	if err := bundle.ledger.CreditPurchase(ctx, u.ID, seedDepositForBalance(t, ctx, s, u.ID), 2); err != nil {
		t.Fatalf("failed to credit purchase: %v", err)
	}

	unauthReq := httptest.NewRequest(http.MethodGet, "/history", nil)
	unauthRec := httptest.NewRecorder()
	bundle.server.Handler().ServeHTTP(unauthRec, unauthReq)
	if unauthRec.Code != http.StatusFound {
		t.Fatalf("expected redirect for unauthenticated /history, got %d", unauthRec.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/history", nil)
	req.AddCookie(sessionCookie(t, bundle.sessions, u.ID, false))
	rec := httptest.NewRecorder()
	bundle.server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "purchase") {
		t.Errorf("expected purchase entry in history, got: %s", rec.Body.String())
	}
}
