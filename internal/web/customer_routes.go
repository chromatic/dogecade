package web

import (
	"context"
	"errors"
	"html/template"
	"net/http"
	"time"

	"github.com/chromatic/dogecade/internal/auth"
	"github.com/chromatic/dogecade/internal/services"
)

// CustomerDeps bundles the services the customer-facing pages need. All
// fields are required; RegisterCustomerRoutes panics (at startup, via a
// nil-pointer deref on first use) if any of the service dependencies are
// left nil, since there's no meaningful degraded mode for the buy/redeem
// flow to fall back to.
type CustomerDeps struct {
	Auth       *auth.Handlers
	Ledger     *services.LedgerService
	Purchase   *services.PurchaseService
	Redemption *services.RedemptionService
	Machines   *services.MachinesService
	Settings   *services.SettingsService
	DirectPay  *services.DirectPayService
}

// RegisterCustomerRoutes parses the embedded page templates and registers
// the customer-facing routes (/, /buy, /machines, /m/{slug}, /history) plus
// the OIDC auth routes and the /static/ file server, on top of whatever
// routes NewServer already registered (currently just /healthz).
func (s *Server) RegisterCustomerRoutes(deps CustomerDeps) error {
	pages, err := parsePageTemplates()
	if err != nil {
		return err
	}
	s.pages = pages
	s.customerDeps = &deps

	s.mux.Handle("GET /static/", http.FileServerFS(staticFS))

	s.mux.HandleFunc("GET /auth/login", deps.Auth.Login)
	s.mux.HandleFunc("GET /auth/callback", deps.Auth.Callback)
	s.mux.HandleFunc("POST /auth/logout", deps.Auth.Logout)

	s.mux.Handle("GET /{$}", deps.Auth.OptionalAuth(http.HandlerFunc(s.handleIndex)))
	s.mux.HandleFunc("GET /machines", s.handleMachinesList)
	s.mux.Handle("GET /m/{slug}", deps.Auth.OptionalAuth(http.HandlerFunc(s.handleMachineShow)))
	s.mux.Handle("POST /m/{slug}", deps.Auth.RequireAuth(http.HandlerFunc(s.handleMachineRedeem)))
	s.mux.Handle("GET /buy", deps.Auth.RequireAuth(http.HandlerFunc(s.handleBuy)))
	s.mux.HandleFunc("GET /buy/status", s.handleBuyStatus)
	s.mux.HandleFunc("GET /buy/events", s.handleBuyEvents)
	s.mux.Handle("GET /history", deps.Auth.RequireAuth(http.HandlerFunc(s.handleHistory)))

	return nil
}

// sessionUser builds the *services.User a template needs from the request's
// session, without a database round trip: the session already carries the
// user ID and admin flag, and no page currently renders the display name.
func sessionUser(r *http.Request) *services.User {
	sess, err := auth.CurrentSession(r)
	if err != nil {
		return nil
	}
	return &services.User{ID: sess.UserID, IsAdmin: sess.IsAdmin}
}

func (s *Server) renderPage(w http.ResponseWriter, name string, data any) {
	tmpl, ok := s.pages[name]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "layout", data); err != nil {
		s.logger.Error("failed to render page", "template", name, "err", err)
	}
}

type indexPageData struct {
	User    *services.User
	Balance int64
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	user := sessionUser(r)
	var balance int64
	if user != nil {
		bal, err := s.customerDeps.Ledger.Balance(r.Context(), user.ID)
		if err != nil {
			s.logger.Error("failed to load balance", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		balance = bal
	}
	s.renderPage(w, "index.html", indexPageData{User: user, Balance: balance})
}

type machinesPageData struct {
	User     *services.User
	Machines []services.Machine
}

func (s *Server) handleMachinesList(w http.ResponseWriter, r *http.Request) {
	machines, err := s.customerDeps.Machines.ListActive(r.Context())
	if err != nil {
		s.logger.Error("failed to list machines", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderPage(w, "machines.html", machinesPageData{User: sessionUser(r), Machines: machines})
}

type machinePageData struct {
	User             *services.User
	Machine          services.Machine
	Balance          int64
	Redeemed         bool
	Error            string
	DirectPayAddress string
	// DirectPayQRDataURI is template.URL for the same autoescaper reason as
	// buyPageData.QRDataURI (see handleBuy).
	DirectPayQRDataURI template.URL
}

// directPayPageFields loads the active direct-pay address and QR for a
// machine, if direct pay is enabled for it. It's best-effort: a lookup
// failure just omits the direct-pay section rather than failing the whole
// page, since the redemption path doesn't depend on it.
func (s *Server) directPayPageFields(ctx context.Context, machine services.Machine) (address string, qr template.URL) {
	if !machine.DirectPayEnabled || s.customerDeps.DirectPay == nil {
		return "", ""
	}
	active, ok, err := s.customerDeps.DirectPay.ActiveAddress(ctx, machine.ID)
	if err != nil || !ok {
		if err != nil {
			s.logger.Error("failed to load direct-pay address", "machine_id", machine.ID, "err", err)
		}
		return "", ""
	}
	dataURI, err := qrDataURI("dogecoin:"+active.Address, 240)
	if err != nil {
		s.logger.Error("failed to render direct-pay QR code", "err", err)
		return active.Address, ""
	}
	return active.Address, template.URL(dataURI)
}

func (s *Server) handleMachineShow(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	machine, err := s.customerDeps.Machines.GetBySlug(r.Context(), slug)
	if errors.Is(err, services.ErrMachineSlugNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.logger.Error("failed to look up machine", "slug", slug, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	user := sessionUser(r)
	var balance int64
	if user != nil {
		bal, err := s.customerDeps.Ledger.Balance(r.Context(), user.ID)
		if err != nil {
			s.logger.Error("failed to load balance", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		balance = bal
	}

	directPayAddress, directPayQR := s.directPayPageFields(r.Context(), machine)
	s.renderPage(w, "machine.html", machinePageData{
		User:               user,
		Machine:            machine,
		Balance:            balance,
		DirectPayAddress:   directPayAddress,
		DirectPayQRDataURI: directPayQR,
	})
}

func (s *Server) handleMachineRedeem(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	machine, err := s.customerDeps.Machines.GetBySlug(r.Context(), slug)
	if errors.Is(err, services.ErrMachineSlugNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.logger.Error("failed to look up machine", "slug", slug, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	sess, err := auth.CurrentSession(r)
	if err != nil {
		// RequireAuth already guarantees a session; this would only happen
		// if the middleware chain changes underneath us.
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}
	user := &services.User{ID: sess.UserID, IsAdmin: sess.IsAdmin}

	directPayAddress, directPayQR := s.directPayPageFields(r.Context(), machine)
	data := machinePageData{User: user, Machine: machine, DirectPayAddress: directPayAddress, DirectPayQRDataURI: directPayQR}

	_, err = s.customerDeps.Redemption.Redeem(r.Context(), sess.UserID, machine.ID)
	switch {
	case err == nil:
		data.Redeemed = true
	case errors.Is(err, services.ErrInsufficientBalance):
		data.Error = "You don't have any tokens left. Buy more to keep playing."
	case errors.Is(err, services.ErrMachineNotActive):
		data.Error = "This machine is currently disabled."
	case errors.Is(err, services.ErrMachineNotFound):
		http.NotFound(w, r)
		return
	default:
		s.logger.Error("redemption failed", "machine_id", machine.ID, "user_id", sess.UserID, "err", err)
		data.Error = "Something went wrong redeeming your token. Please try again."
	}

	balance, err := s.customerDeps.Ledger.Balance(r.Context(), sess.UserID)
	if err == nil {
		data.Balance = balance
	}

	s.renderPage(w, "machine.html", data)
}

type buyPageData struct {
	User        *services.User
	Paused      bool
	PauseReason string
	Address     string
	// QRDataURI is template.URL (not string) so html/template's contextual
	// autoescaper treats the data: URI as a trusted URL instead of
	// replacing it with "#ZgotmplZ" — it only does that for non-http(s)
	// schemes in a src attribute when given a plain string.
	QRDataURI template.URL
}

// nodeStatePauseReason maps a chain.NodeState string to the customer-facing
// explanation shown on /buy when purchasing is paused. "" (empty checker
// result) is treated the same as "unconfigured".
func nodeStatePauseReason(state string) string {
	switch state {
	case "unreachable":
		return "We can't reach the Dogecoin node right now, so incoming payments can't be tracked. Please try again shortly."
	case "syncing":
		return "The arcade's Dogecoin node is still syncing the blockchain. Buying is paused until it catches up."
	case "unconfigured", "":
		return "The arcade's Dogecoin node isn't configured yet. Please check back later."
	default:
		return ""
	}
}

func (s *Server) handleBuy(w http.ResponseWriter, r *http.Request) {
	sess, err := auth.CurrentSession(r)
	if err != nil {
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}
	user := &services.User{ID: sess.UserID, IsAdmin: sess.IsAdmin}

	nodeState := "unconfigured"
	if s.checker != nil {
		state, err := s.checker.Check(r.Context())
		if err != nil {
			s.logger.Error("failed to check node health for purchase pause", "err", err)
		} else {
			nodeState = state
		}
	}
	if nodeState != "ok" {
		s.renderPage(w, "buy.html", buyPageData{User: user, Paused: true, PauseReason: nodeStatePauseReason(nodeState)})
		return
	}

	address, err := s.customerDeps.Purchase.StartPurchase(r.Context(), sess.UserID)
	if errors.Is(err, services.ErrPoolEmpty) {
		s.renderPage(w, "buy.html", buyPageData{
			User:        user,
			Paused:      true,
			PauseReason: "We're out of available deposit addresses right now. An admin has been alerted.",
		})
		return
	}
	if err != nil {
		s.logger.Error("failed to start purchase", "user_id", sess.UserID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	qr, err := qrDataURI("dogecoin:"+address, 240)
	if err != nil {
		s.logger.Error("failed to render QR code", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	s.renderPage(w, "buy.html", buyPageData{User: user, Address: address, QRDataURI: template.URL(qr)})
}

func (s *Server) handleBuyStatus(w http.ResponseWriter, r *http.Request) {
	address := r.URL.Query().Get("address")
	if address == "" {
		http.Error(w, "missing address", http.StatusBadRequest)
		return
	}
	minConf, err := s.customerDeps.Settings.GetMinConfirmations(r.Context())
	if err != nil {
		s.logger.Error("failed to load min_confirmations", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	status, err := latestDepositStatus(r.Context(), s.store.DB(), address, minConf)
	if err != nil {
		s.logger.Error("failed to load deposit status", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, status)
}

// buyEventsMaxDuration bounds how long an SSE connection is held open, so a
// customer who leaves the /buy tab open forever doesn't pin a goroutine and
// a DB-polling ticker indefinitely; the client-side JS falls back to
// polling if the connection drops before the payment completes.
const buyEventsMaxDuration = 15 * time.Minute

func (s *Server) handleBuyEvents(w http.ResponseWriter, r *http.Request) {
	address := r.URL.Query().Get("address")
	if address == "" {
		http.Error(w, "missing address", http.StatusBadRequest)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	minConf, err := s.customerDeps.Settings.GetMinConfirmations(r.Context())
	if err != nil {
		s.logger.Error("failed to load min_confirmations", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	ctx, cancel := context.WithTimeout(r.Context(), buyEventsMaxDuration)
	defer cancel()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	var lastState string
	for {
		status, err := latestDepositStatus(ctx, s.store.DB(), address, minConf)
		if err != nil {
			s.logger.Error("failed to load deposit status for SSE", "err", err)
		} else if status.State != lastState {
			lastState = status.State
			if err := writeSSEEvent(w, status); err != nil {
				return
			}
			flusher.Flush()
			if status.State == "credited" {
				return
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

type historyPageData struct {
	User    *services.User
	Entries []services.LedgerEntry
}

const historyPageSize = 100

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	sess, err := auth.CurrentSession(r)
	if err != nil {
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}
	entries, err := s.customerDeps.Ledger.History(r.Context(), sess.UserID, historyPageSize)
	if err != nil {
		s.logger.Error("failed to load ledger history", "user_id", sess.UserID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderPage(w, "history.html", historyPageData{
		User:    &services.User{ID: sess.UserID, IsAdmin: sess.IsAdmin},
		Entries: entries,
	})
}
