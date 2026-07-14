package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/chromatic/dogecade/internal/auth"
	"github.com/chromatic/dogecade/internal/services"
	"github.com/chromatic/dogecade/internal/store"
)

type fakeTestFirer struct {
	err        error
	lastCalled int64
}

func (f *fakeTestFirer) TestFire(ctx context.Context, machineID int64) error {
	f.lastCalled = machineID
	return f.err
}

type adminTestBundle struct {
	server    *Server
	sessions  *auth.SessionManager
	users     *services.UsersService
	ledger    *services.LedgerService
	settings  *services.SettingsService
	pool      *services.PoolService
	machines  *services.MachinesService
	relays    *services.RelaysService
	directPay *services.DirectPayService
	firer     *fakeTestFirer
}

func newAdminTestBundle(t *testing.T) (*store.Store, adminTestBundle) {
	t.Helper()
	s := openTestStore(t)

	users := services.NewUsersService(s)
	ledger := services.NewLedgerService(s)
	settings := services.NewSettingsService(s)
	purchase := services.NewPurchaseService(s)
	redemption := services.NewRedemptionService(s, ledger)
	machines := services.NewMachinesService(s)
	pool := services.NewPoolService(s, settings)
	addressBatches := services.NewAddressBatchService(s)
	relays := services.NewRelaysService(s)
	deposits := services.NewDepositsService(s)
	alerts := services.NewAlertsService(s)
	audit := services.NewAdminAuditService(s)
	directPay := services.NewDirectPayService(s, settings)

	sessions := auth.NewSessionManager([]byte("test-secret"), false)
	authHandlers := auth.NewHandlers(sessions, nil, users, nil, false)

	firer := &fakeTestFirer{}

	server := NewServer(s, nil, nil)
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
	if err := server.RegisterAdminRoutes(AdminDeps{
		Auth:           authHandlers,
		Settings:       settings,
		Pool:           pool,
		AddressBatches: addressBatches,
		Machines:       machines,
		Relays:         relays,
		Deposits:       deposits,
		Users:          users,
		Ledger:         ledger,
		Alerts:         alerts,
		Audit:          audit,
		Dispatcher:     firer,
		DirectPay:      directPay,
		BaseURL:        "https://arcade.example.com",
	}); err != nil {
		t.Fatalf("RegisterAdminRoutes failed: %v", err)
	}

	return s, adminTestBundle{
		server: server, sessions: sessions, users: users, ledger: ledger,
		settings: settings, pool: pool, machines: machines, relays: relays,
		directPay: directPay, firer: firer,
	}
}

func adminSessionCookie(t *testing.T, sessions *auth.SessionManager, userID int64) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := sessions.Issue(rec, userID, true); err != nil {
		t.Fatalf("Issue failed: %v", err)
	}
	return rec.Result().Cookies()[0]
}

func TestAdminDashboardRequiresAdmin(t *testing.T) {
	s, bundle := newAdminTestBundle(t)
	ctx := context.Background()
	userID := seedNonAdminUser(t, ctx, s, "not-an-admin")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(sessionCookieAsNonAdmin(t, bundle.sessions, userID))
	bundle.server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin, got %d", rec.Code)
	}
}

func sessionCookieAsNonAdmin(t *testing.T, sessions *auth.SessionManager, userID int64) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := sessions.Issue(rec, userID, false); err != nil {
		t.Fatalf("Issue failed: %v", err)
	}
	return rec.Result().Cookies()[0]
}

func seedUser(t *testing.T, ctx context.Context, s *store.Store, subjectHash string) int64 {
	t.Helper()
	users := services.NewUsersService(s)
	// isAdmin=true: seedUser backs adminSessionCookie in these tests, and
	// RequireAdmin now re-checks is_admin against the DB record (not just
	// the session cookie), so the seeded user must actually be an admin.
	u, err := users.GetOrCreateBySubjectHash(ctx, subjectHash, "Test User", true)
	if err != nil {
		t.Fatalf("failed to seed user: %v", err)
	}
	return u.ID
}

func seedNonAdminUser(t *testing.T, ctx context.Context, s *store.Store, subjectHash string) int64 {
	t.Helper()
	users := services.NewUsersService(s)
	u, err := users.GetOrCreateBySubjectHash(ctx, subjectHash, "Test User", false)
	if err != nil {
		t.Fatalf("failed to seed user: %v", err)
	}
	return u.ID
}

func TestAdminDashboardRendersForAdmin(t *testing.T) {
	s, bundle := newAdminTestBundle(t)
	ctx := context.Background()
	adminID := seedUser(t, ctx, s, "admin-subject")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(adminSessionCookie(t, bundle.sessions, adminID))
	bundle.server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Admin dashboard") {
		t.Errorf("expected dashboard heading, got body: %s", rec.Body.String())
	}
}

func TestAdminMachineCreateAndToggle(t *testing.T) {
	s, bundle := newAdminTestBundle(t)
	ctx := context.Background()
	adminID := seedUser(t, ctx, s, "admin-subject")
	cookie := adminSessionCookie(t, bundle.sessions, adminID)

	form := url.Values{"slug": {"pinball-1"}, "name": {"Pinball One"}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/machines", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	bundle.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after create, got %d: %s", rec.Code, rec.Body.String())
	}

	machines, err := bundle.machines.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll failed: %v", err)
	}
	if len(machines) != 1 || machines[0].Slug != "pinball-1" || !machines[0].IsActive {
		t.Fatalf("expected one active machine 'pinball-1', got %+v", machines)
	}

	toggleForm := url.Values{"active": {"0"}}
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/admin/machines/"+itoa(machines[0].ID)+"/toggle", strings.NewReader(toggleForm.Encode()))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req2.AddCookie(cookie)
	bundle.server.Handler().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after toggle, got %d", rec2.Code)
	}

	machines, err = bundle.machines.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll failed: %v", err)
	}
	if machines[0].IsActive {
		t.Fatalf("expected machine to be disabled after toggle")
	}
}

func TestAdminMachinesListShowsQRThumbnailAndPrintLink(t *testing.T) {
	s, bundle := newAdminTestBundle(t)
	ctx := context.Background()
	adminID := seedUser(t, ctx, s, "admin-subject")
	cookie := adminSessionCookie(t, bundle.sessions, adminID)

	id, err := bundle.machines.Create(ctx, "pinball-1", "Pinball One")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/machines", nil)
	req.AddCookie(cookie)
	bundle.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "data:image/png;base64,") {
		t.Fatalf("expected a QR thumbnail data URI in machines list, got:\n%s", body)
	}
	printLink := "/admin/machines/" + itoa(id) + "/qr"
	if !strings.Contains(body, printLink) {
		t.Fatalf("expected a print-QR link %q in machines list, got:\n%s", printLink, body)
	}
}

func TestAdminMachineQRPageShowsAbsoluteURL(t *testing.T) {
	s, bundle := newAdminTestBundle(t)
	ctx := context.Background()
	adminID := seedUser(t, ctx, s, "admin-subject")
	cookie := adminSessionCookie(t, bundle.sessions, adminID)

	id, err := bundle.machines.Create(ctx, "pinball-1", "Pinball One")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/machines/"+itoa(id)+"/qr", nil)
	req.AddCookie(cookie)
	bundle.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	wantURL := "https://arcade.example.com/m/pinball-1"
	if !strings.Contains(body, wantURL) {
		t.Fatalf("expected page to show %q, got:\n%s", wantURL, body)
	}
	if !strings.Contains(body, "data:image/png;base64,") {
		t.Fatalf("expected a QR image in the response, got:\n%s", body)
	}
}

func TestAdminMachineQRPageNotFoundForUnknownID(t *testing.T) {
	s, bundle := newAdminTestBundle(t)
	ctx := context.Background()
	adminID := seedUser(t, ctx, s, "admin-subject")
	cookie := adminSessionCookie(t, bundle.sessions, adminID)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/machines/999/qr", nil)
	req.AddCookie(cookie)
	bundle.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown machine id, got %d", rec.Code)
	}
}

func TestAdminNodeSettingsPasswordNotEchoedAndPreservedWhenBlank(t *testing.T) {
	s, bundle := newAdminTestBundle(t)
	ctx := context.Background()
	adminID := seedUser(t, ctx, s, "admin-subject")
	cookie := adminSessionCookie(t, bundle.sessions, adminID)

	form := url.Values{
		"rpc_url":  {"http://127.0.0.1:22555"},
		"rpc_user": {"dogecade"},
		"rpc_pass": {"super-secret"},
		"zmq_addr": {""},
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/settings/node", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	bundle.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "super-secret") {
		t.Fatalf("expected RPC password not to be echoed back into the page, got:\n%s", rec.Body.String())
	}

	cfg, err := bundle.settings.GetNodeRPCConfig(ctx)
	if err != nil {
		t.Fatalf("GetNodeRPCConfig failed: %v", err)
	}
	if cfg.RPCPass != "super-secret" {
		t.Fatalf("expected saved RPC password 'super-secret', got %q", cfg.RPCPass)
	}

	// Re-save with rpc_pass left blank: the password should be preserved,
	// not cleared.
	form2 := url.Values{
		"rpc_url":  {"http://127.0.0.1:22556"},
		"rpc_user": {"dogecade"},
		"rpc_pass": {""},
		"zmq_addr": {""},
	}
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/admin/settings/node", strings.NewReader(form2.Encode()))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req2.AddCookie(cookie)
	bundle.server.Handler().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}

	cfg2, err := bundle.settings.GetNodeRPCConfig(ctx)
	if err != nil {
		t.Fatalf("GetNodeRPCConfig failed: %v", err)
	}
	if cfg2.RPCPass != "super-secret" {
		t.Fatalf("expected RPC password preserved as 'super-secret' after blank re-save, got %q", cfg2.RPCPass)
	}
	if cfg2.RPCURL != "http://127.0.0.1:22556" {
		t.Fatalf("expected other fields to still update, got RPCURL=%q", cfg2.RPCURL)
	}
}

func TestAdminNodeCheckUsesSubmittedFormValuesNotStaleSavedOnes(t *testing.T) {
	s, bundle := newAdminTestBundle(t)
	ctx := context.Background()
	adminID := seedUser(t, ctx, s, "admin-subject")
	cookie := adminSessionCookie(t, bundle.sessions, adminID)

	saveForm := url.Values{
		"rpc_url":  {"http://127.0.0.1:1"},
		"rpc_user": {"dogecade"},
		"rpc_pass": {"secret"},
		"zmq_addr": {""},
	}
	saveRec := httptest.NewRecorder()
	saveReq := httptest.NewRequest(http.MethodPost, "/admin/settings/node", strings.NewReader(saveForm.Encode()))
	saveReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	saveReq.AddCookie(cookie)
	bundle.server.Handler().ServeHTTP(saveRec, saveReq)
	if saveRec.Code != http.StatusOK {
		t.Fatalf("expected 200 from save, got %d: %s", saveRec.Code, saveRec.Body.String())
	}

	// Check against a *different*, unsaved URL/port: the response should
	// reflect an attempt against the submitted port (2), not the saved one
	// (1), and the save should not have persisted this second port.
	checkForm := url.Values{
		"rpc_url":  {"http://127.0.0.1:2"},
		"rpc_user": {"dogecade"},
		"rpc_pass": {""},
		"zmq_addr": {""},
	}
	checkRec := httptest.NewRecorder()
	checkReq := httptest.NewRequest(http.MethodPost, "/admin/settings/node/check", strings.NewReader(checkForm.Encode()))
	checkReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	checkReq.AddCookie(cookie)
	bundle.server.Handler().ServeHTTP(checkRec, checkReq)
	if checkRec.Code != http.StatusOK {
		t.Fatalf("expected 200 from check, got %d: %s", checkRec.Code, checkRec.Body.String())
	}
	body := checkRec.Body.String()
	if !strings.Contains(body, "127.0.0.1:2") {
		t.Fatalf("expected check-connection error to reference the submitted port 2, got:\n%s", body)
	}
	if strings.Contains(body, "secret") {
		t.Fatalf("expected RPC password not to be echoed back on the check page, got:\n%s", body)
	}

	cfg, err := bundle.settings.GetNodeRPCConfig(ctx)
	if err != nil {
		t.Fatalf("GetNodeRPCConfig failed: %v", err)
	}
	if cfg.RPCURL != "http://127.0.0.1:1" {
		t.Fatalf("expected check to not persist its values, saved RPCURL should still be port 1, got %q", cfg.RPCURL)
	}
	if cfg.RPCPass != "secret" {
		t.Fatalf("expected saved password to be untouched by check, got %q", cfg.RPCPass)
	}
}

func TestAdminMachinesPageRelayBindFormUsesDropdowns(t *testing.T) {
	s, bundle := newAdminTestBundle(t)
	ctx := context.Background()
	adminID := seedUser(t, ctx, s, "admin-subject")
	cookie := adminSessionCookie(t, bundle.sessions, adminID)

	machineID, err := bundle.machines.Create(ctx, "pinball-1", "Pinball One")
	if err != nil {
		t.Fatalf("Create machine failed: %v", err)
	}
	boardID, err := bundle.relays.CreateBoard(ctx, "Board One", "http://relay1.lan")
	if err != nil {
		t.Fatalf("CreateBoard failed: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/machines", nil)
	req.AddCookie(cookie)
	bundle.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `<select name="machine_id" required>`) {
		t.Fatalf("expected a machine_id select dropdown, got:\n%s", body)
	}
	if !strings.Contains(body, `<select name="board_id" required>`) {
		t.Fatalf("expected a board_id select dropdown, got:\n%s", body)
	}
	wantMachineOption := `<option value="` + itoa(machineID) + `">Pinball One (pinball-1)</option>`
	if !strings.Contains(body, wantMachineOption) {
		t.Fatalf("expected machine option %q, got:\n%s", wantMachineOption, body)
	}
	wantBoardOption := `<option value="` + itoa(boardID) + `">Board One (http://relay1.lan)</option>`
	if !strings.Contains(body, wantBoardOption) {
		t.Fatalf("expected board option %q, got:\n%s", wantBoardOption, body)
	}
}

func TestAdminSettingsSaveRoundTrip(t *testing.T) {
	s, bundle := newAdminTestBundle(t)
	ctx := context.Background()
	adminID := seedUser(t, ctx, s, "admin-subject")
	cookie := adminSessionCookie(t, bundle.sessions, adminID)

	form := url.Values{
		"min_confirmations":                {"3"},
		"zero_conf_max_koinu":              {"0"},
		"pool_warn_threshold":              {"20"},
		"pool_urgent_threshold":            {"5"},
		"token_price_koinu":                {"50000000"},
		"relay_pulse_gap_ms":               {"1000"},
		"relay_max_attempts":               {"4"},
		"direct_pay_max_credits_per_tx":    {"10"},
		"direct_pay_rotate_interval_hours": {"0"},
		"direct_pay_rotate_after_uses":     {"0"},
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/settings", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	bundle.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	minConf, err := bundle.settings.GetMinConfirmations(ctx)
	if err != nil || minConf != 3 {
		t.Fatalf("expected min_confirmations=3, got %d (err %v)", minConf, err)
	}
}

func TestAdminSettingsSaveRejectsInvalidInput(t *testing.T) {
	s, bundle := newAdminTestBundle(t)
	ctx := context.Background()
	adminID := seedUser(t, ctx, s, "admin-subject")
	cookie := adminSessionCookie(t, bundle.sessions, adminID)

	form := url.Values{
		"min_confirmations":     {"not-a-number"},
		"zero_conf_max_koinu":   {"0"},
		"pool_warn_threshold":   {"20"},
		"pool_urgent_threshold": {"5"},
		"token_price_koinu":     {"50000000"},
		"relay_pulse_gap_ms":    {"1000"},
		"relay_max_attempts":    {"4"},
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/settings", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	bundle.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "must be") {
		t.Fatalf("expected validation error rendered, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAdminAddressesImportAndRetire(t *testing.T) {
	s, bundle := newAdminTestBundle(t)
	ctx := context.Background()
	adminID := seedUser(t, ctx, s, "admin-subject")
	cookie := adminSessionCookie(t, bundle.sessions, adminID)

	form := url.Values{"addresses": {"# a comment\nDTestAddr111111111111111111111111\nDTestAddr222222222222222222222222\n"}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/addresses/import", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	bundle.server.Handler().ServeHTTP(rec, req)
	// Addresses aren't valid Dogecoin addresses, so ImportBatch should reject
	// them; assert the page re-renders with an error rather than 500ing.
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with inline error for invalid addresses, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Import failed") {
		t.Errorf("expected import error message, got body: %s", rec.Body.String())
	}
}

func TestAdminUserAdjustRequiresNote(t *testing.T) {
	s, bundle := newAdminTestBundle(t)
	ctx := context.Background()
	adminID := seedUser(t, ctx, s, "admin-subject")
	targetID := seedUser(t, ctx, s, "target-subject")
	cookie := adminSessionCookie(t, bundle.sessions, adminID)

	form := url.Values{"delta": {"5"}, "note": {""}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/users/"+itoa(targetID)+"/adjust", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	bundle.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "note is required") {
		t.Fatalf("expected note-required error, got %d: %s", rec.Code, rec.Body.String())
	}

	balance, err := bundle.ledger.Balance(ctx, targetID)
	if err != nil || balance != 0 {
		t.Fatalf("expected balance to remain 0 without a note, got %d (err %v)", balance, err)
	}
}

func TestAdminUserAdjustAndMerge(t *testing.T) {
	s, bundle := newAdminTestBundle(t)
	ctx := context.Background()
	adminID := seedUser(t, ctx, s, "admin-subject")
	fromID := seedUser(t, ctx, s, "from-subject")
	toID := seedUser(t, ctx, s, "to-subject")
	cookie := adminSessionCookie(t, bundle.sessions, adminID)

	adjustForm := url.Values{"delta": {"7"}, "note": {"manual credit for testing"}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/users/"+itoa(fromID)+"/adjust", strings.NewReader(adjustForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	bundle.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after adjust, got %d: %s", rec.Code, rec.Body.String())
	}

	mergeForm := url.Values{"from_id": {itoa(fromID)}}
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/admin/users/"+itoa(toID)+"/merge", strings.NewReader(mergeForm.Encode()))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req2.AddCookie(cookie)
	bundle.server.Handler().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after merge, got %d: %s", rec2.Code, rec2.Body.String())
	}

	toBalance, err := bundle.ledger.Balance(ctx, toID)
	if err != nil || toBalance != 7 {
		t.Fatalf("expected merged balance of 7, got %d (err %v)", toBalance, err)
	}
	if _, err := bundle.users.GetByID(ctx, fromID); err == nil {
		t.Fatalf("expected merged-from user to be deleted")
	}
}

func TestAdminTestFire(t *testing.T) {
	s, bundle := newAdminTestBundle(t)
	ctx := context.Background()
	adminID := seedUser(t, ctx, s, "admin-subject")
	machineID := seedMachine(t, ctx, s, "test-machine", true)
	cookie := adminSessionCookie(t, bundle.sessions, adminID)

	form := url.Values{"machine_id": {itoa(machineID)}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/relays/test-fire", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	bundle.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if bundle.firer.lastCalled != machineID {
		t.Errorf("expected TestFire called with machine %d, got %d", machineID, bundle.firer.lastCalled)
	}
}

func itoa(id int64) string {
	return strconv.FormatInt(id, 10)
}

func TestAdminMachineDirectPayEnableActivatesAddressAndCustomerPageShowsQR(t *testing.T) {
	s, bundle := newAdminTestBundle(t)
	ctx := context.Background()
	adminID := seedUser(t, ctx, s, "admin-subject")
	machineID := seedMachine(t, ctx, s, "direct-pay-machine", true)
	cookie := adminSessionCookie(t, bundle.sessions, adminID)

	seedPoolAddress(t, ctx, s, "DAdminDirectPayAddr1", "machine_direct")

	form := url.Values{"enabled": {"1"}, "price_koinu": {"100000000"}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/machines/"+itoa(machineID)+"/direct-pay", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	bundle.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after enabling direct pay, got %d: %s", rec.Code, rec.Body.String())
	}

	active, ok, err := bundle.directPay.ActiveAddress(ctx, machineID)
	if err != nil || !ok || active.Address != "DAdminDirectPayAddr1" {
		t.Fatalf("expected active direct-pay address, got %+v ok=%v err=%v", active, ok, err)
	}

	machine, err := bundle.machines.GetByID(ctx, machineID)
	if err != nil {
		t.Fatalf("GetByID failed: %v", err)
	}
	if !machine.DirectPayEnabled || machine.DirectPlayPriceKoinu != 100000000 {
		t.Fatalf("expected direct pay enabled with price 100000000, got %+v", machine)
	}

	pageRec := httptest.NewRecorder()
	pageReq := httptest.NewRequest(http.MethodGet, "/m/direct-pay-machine", nil)
	bundle.server.Handler().ServeHTTP(pageRec, pageReq)
	if pageRec.Code != http.StatusOK {
		t.Fatalf("expected 200 from machine page, got %d", pageRec.Code)
	}
	body := pageRec.Body.String()
	if !strings.Contains(body, "DAdminDirectPayAddr1") {
		t.Fatalf("expected machine page to show the direct-pay address, got body: %s", body)
	}
	if !strings.Contains(body, "1 DOGE") {
		t.Fatalf("expected machine page to show the direct-pay price in DOGE, got body: %s", body)
	}
	if !strings.Contains(body, `data-mode="direct"`) {
		t.Fatalf("expected machine page to wire up live payment status for direct pay, got body: %s", body)
	}
	if !strings.Contains(body, "isn't refunded or carried over") {
		t.Fatalf("expected machine page to warn that overpayment isn't refunded, got body: %s", body)
	}
}

func TestAdminMachineDirectPayRotate(t *testing.T) {
	s, bundle := newAdminTestBundle(t)
	ctx := context.Background()
	adminID := seedUser(t, ctx, s, "admin-subject")
	machineID := seedMachine(t, ctx, s, "rotate-machine", true)
	cookie := adminSessionCookie(t, bundle.sessions, adminID)

	seedPoolAddress(t, ctx, s, "DRotateAddrOld", "machine_direct")
	if err := bundle.machines.SetDirectPay(ctx, machineID, true, 100000000); err != nil {
		t.Fatalf("SetDirectPay setup failed: %v", err)
	}
	if _, err := bundle.directPay.Activate(ctx, machineID); err != nil {
		t.Fatalf("Activate setup failed: %v", err)
	}
	seedPoolAddress(t, ctx, s, "DRotateAddrNew", "machine_direct")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/machines/"+itoa(machineID)+"/direct-pay/rotate", nil)
	req.AddCookie(cookie)
	bundle.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after rotate, got %d: %s", rec.Code, rec.Body.String())
	}

	active, ok, err := bundle.directPay.ActiveAddress(ctx, machineID)
	if err != nil || !ok || active.Address != "DRotateAddrNew" {
		t.Fatalf("expected rotation to DRotateAddrNew, got %+v ok=%v err=%v", active, ok, err)
	}
}

func TestAdminMachinesPageRemembersPriceAfterDisablingDirectPay(t *testing.T) {
	s, bundle := newAdminTestBundle(t)
	ctx := context.Background()
	adminID := seedUser(t, ctx, s, "admin-subject")
	machineID := seedMachine(t, ctx, s, "toggle-machine", true)
	cookie := adminSessionCookie(t, bundle.sessions, adminID)

	if err := bundle.machines.SetDirectPay(ctx, machineID, true, 100000000); err != nil {
		t.Fatalf("SetDirectPay(enable) failed: %v", err)
	}
	if err := bundle.machines.SetDirectPay(ctx, machineID, false, 0); err != nil {
		t.Fatalf("SetDirectPay(disable) failed: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/machines", nil)
	req.AddCookie(cookie)
	bundle.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "last: 1 DOGE") {
		t.Fatalf("expected the machines page to show the last-known price after disabling, got:\n%s", body)
	}
	if !strings.Contains(body, `value="100000000"`) {
		t.Fatalf("expected the re-enable price field to be prefilled with the last price, got:\n%s", body)
	}
}
