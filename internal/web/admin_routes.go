package web

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/chromatic/dogecade/internal/auth"
	"github.com/chromatic/dogecade/internal/chain/corerpc"
	"github.com/chromatic/dogecade/internal/services"
)

// relayTestFirer is the subset of *relay.Dispatcher the admin console needs,
// narrowed so tests can fake it without a real Tasmota board.
type relayTestFirer interface {
	TestFire(ctx context.Context, machineID int64) error
}

// AdminDeps bundles the services the admin console needs. NodeImporter may
// be nil (no node configured); every other field is required.
type AdminDeps struct {
	Auth           *auth.Handlers
	Settings       *services.SettingsService
	Pool           *services.PoolService
	AddressBatches *services.AddressBatchService
	Machines       *services.MachinesService
	Relays         *services.RelaysService
	Deposits       *services.DepositsService
	Users          *services.UsersService
	Ledger         *services.LedgerService
	Alerts         *services.AlertsService
	Audit          *services.AdminAuditService
	Dispatcher     relayTestFirer
	NodeImporter   services.NodeImporter
	DirectPay      *services.DirectPayService
	// BaseURL is the deployment's public base URL (DOGECADE_BASE_URL),
	// used to build the absolute /m/{slug} link encoded in each machine's
	// QR code. Printed/scanned QR codes need an absolute URL — a relative
	// path means nothing to a phone camera.
	BaseURL string
}

// RegisterAdminRoutes parses the admin page templates and registers every
// /admin/* route, gated by deps.Auth.RequireAdmin. Must be called after
// RegisterCustomerRoutes (it reuses s.pages, extending it with the admin
// page set).
func (s *Server) RegisterAdminRoutes(deps AdminDeps) error {
	pages, err := parseAdminPageTemplates()
	if err != nil {
		return err
	}
	for name, tmpl := range pages {
		s.pages[name] = tmpl
	}
	s.adminDeps = &deps

	admin := deps.Auth.RequireAdmin

	s.mux.Handle("GET /admin", admin(http.HandlerFunc(s.handleAdminDashboard)))
	s.mux.Handle("POST /admin/alerts/{id}/ack", admin(http.HandlerFunc(s.handleAdminAlertAck)))

	s.mux.Handle("GET /admin/settings", admin(http.HandlerFunc(s.handleAdminSettingsShow)))
	s.mux.Handle("POST /admin/settings", admin(http.HandlerFunc(s.handleAdminSettingsSave)))
	s.mux.Handle("GET /admin/settings/node", admin(http.HandlerFunc(s.handleAdminNodeSettingsShow)))
	s.mux.Handle("POST /admin/settings/node", admin(http.HandlerFunc(s.handleAdminNodeSettingsSave)))
	s.mux.Handle("POST /admin/settings/node/check", admin(http.HandlerFunc(s.handleAdminNodeCheck)))

	s.mux.Handle("GET /admin/machines", admin(http.HandlerFunc(s.handleAdminMachinesShow)))
	s.mux.Handle("POST /admin/machines", admin(http.HandlerFunc(s.handleAdminMachineCreate)))
	s.mux.Handle("POST /admin/machines/{id}/edit", admin(http.HandlerFunc(s.handleAdminMachineEdit)))
	s.mux.Handle("POST /admin/machines/{id}/toggle", admin(http.HandlerFunc(s.handleAdminMachineToggle)))
	s.mux.Handle("GET /admin/machines/{id}/qr", admin(http.HandlerFunc(s.handleAdminMachineQR)))
	s.mux.Handle("POST /admin/boards", admin(http.HandlerFunc(s.handleAdminBoardCreate)))
	s.mux.Handle("POST /admin/boards/{id}/toggle", admin(http.HandlerFunc(s.handleAdminBoardToggle)))
	s.mux.Handle("POST /admin/relays/bind", admin(http.HandlerFunc(s.handleAdminRelayBind)))
	s.mux.Handle("POST /admin/relays/{id}/unbind", admin(http.HandlerFunc(s.handleAdminRelayUnbind)))
	s.mux.Handle("POST /admin/relays/test-fire", admin(http.HandlerFunc(s.handleAdminTestFire)))
	s.mux.Handle("POST /admin/machines/{id}/direct-pay", admin(http.HandlerFunc(s.handleAdminMachineDirectPaySave)))
	s.mux.Handle("POST /admin/machines/{id}/direct-pay/rotate", admin(http.HandlerFunc(s.handleAdminMachineDirectPayRotate)))

	s.mux.Handle("GET /admin/addresses", admin(http.HandlerFunc(s.handleAdminAddressesShow)))
	s.mux.Handle("POST /admin/addresses/import", admin(http.HandlerFunc(s.handleAdminAddressesImport)))
	s.mux.Handle("POST /admin/addresses/{id}/retire", admin(http.HandlerFunc(s.handleAdminAddressRetire)))

	s.mux.Handle("GET /admin/deposits", admin(http.HandlerFunc(s.handleAdminDepositsShow)))

	s.mux.Handle("GET /admin/users", admin(http.HandlerFunc(s.handleAdminUsersShow)))
	s.mux.Handle("GET /admin/users/{id}", admin(http.HandlerFunc(s.handleAdminUserShow)))
	s.mux.Handle("POST /admin/users/{id}/adjust", admin(http.HandlerFunc(s.handleAdminUserAdjust)))
	s.mux.Handle("POST /admin/users/{id}/merge", admin(http.HandlerFunc(s.handleAdminUserMerge)))

	return nil
}

// adminUser returns the current session's user, which RequireAdmin already
// guarantees is present and an admin.
func adminUser(r *http.Request) *services.User {
	sess, err := auth.CurrentSession(r)
	if err != nil {
		return nil
	}
	return &services.User{ID: sess.UserID, IsAdmin: sess.IsAdmin}
}

func (s *Server) logAdminAction(r *http.Request, action, target, note string) {
	sess, err := auth.CurrentSession(r)
	if err != nil {
		return
	}
	if err := s.adminDeps.Audit.Log(r.Context(), sess.UserID, action, target, note); err != nil {
		s.logger.Error("failed to log admin action", "action", action, "err", err)
	}
}

func (s *Server) adminRedirect(w http.ResponseWriter, r *http.Request, path string) {
	http.Redirect(w, r, path, http.StatusSeeOther)
}

func pathID(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.PathValue("id"), 10, 64)
}

// --- Dashboard (7.2) ---

type adminDashboardData struct {
	User        *services.User
	NodeState   string
	PoolCounts  map[string]int
	Boards      []services.RelayBoard
	Alerts      []services.Alert
	Deposits    []services.Deposit
	Redemptions []recentRedemption
}

const adminDashboardRecentLimit = 20

func (s *Server) handleAdminDashboard(w http.ResponseWriter, r *http.Request) {
	nodeState := "unconfigured"
	if s.checker != nil {
		if state, err := s.checker.Check(r.Context()); err == nil {
			nodeState = state
		}
	}

	poolCounts, err := s.adminDeps.Pool.CountsByState(r.Context())
	if err != nil {
		s.logger.Error("failed to load pool counts", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	boards, err := s.adminDeps.Relays.ListBoards(r.Context())
	if err != nil {
		s.logger.Error("failed to load relay boards", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	alerts, err := s.adminDeps.Alerts.ListUnacked(r.Context())
	if err != nil {
		s.logger.Error("failed to load alerts", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	deposits, err := s.adminDeps.Deposits.List(r.Context(), "", adminDashboardRecentLimit)
	if err != nil {
		s.logger.Error("failed to load recent deposits", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	redemptions, err := recentRedemptions(r.Context(), s.store.DB(), adminDashboardRecentLimit)
	if err != nil {
		s.logger.Error("failed to load recent redemptions", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	s.renderPage(w, "admin_dashboard.html", adminDashboardData{
		User:        adminUser(r),
		NodeState:   nodeState,
		PoolCounts:  poolCounts,
		Boards:      boards,
		Alerts:      alerts,
		Deposits:    deposits,
		Redemptions: redemptions,
	})
}

func (s *Server) handleAdminAlertAck(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.Error(w, "invalid alert id", http.StatusBadRequest)
		return
	}
	if err := s.adminDeps.Alerts.Ack(r.Context(), id); err != nil {
		s.logger.Error("failed to ack alert", "id", id, "err", err)
	} else {
		s.logAdminAction(r, "alert.ack", fmt.Sprintf("alert:%d", id), "")
	}
	s.adminRedirect(w, r, "/admin")
}

// --- Settings (7.7) ---

type adminSettingsData struct {
	User                      *services.User
	MinConfirmations          int
	ZeroConfMaxKoinu          int64
	PoolWarnThreshold         int
	PoolUrgentThreshold       int
	TokenPriceKoinu           int64
	RelayPulseGapMs           int
	RelayMaxAttempts          int
	DirectPayMaxCreditsPerTx  int
	DirectPayRotateIntervalHr int
	DirectPayRotateAfterUses  int
	Error                     string
	Saved                     bool
}

func (s *Server) loadAdminSettingsData(ctx context.Context) (adminSettingsData, error) {
	var d adminSettingsData
	var err error
	if d.MinConfirmations, err = s.adminDeps.Settings.GetMinConfirmations(ctx); err != nil {
		return d, err
	}
	if d.ZeroConfMaxKoinu, err = s.adminDeps.Settings.GetZeroConfMaxKoinu(ctx); err != nil {
		return d, err
	}
	if d.PoolWarnThreshold, err = s.adminDeps.Settings.GetPoolWarnThreshold(ctx); err != nil {
		return d, err
	}
	if d.PoolUrgentThreshold, err = s.adminDeps.Settings.GetPoolUrgentThreshold(ctx); err != nil {
		return d, err
	}
	if d.TokenPriceKoinu, err = s.adminDeps.Settings.GetTokenPriceKoinu(ctx); err != nil {
		return d, err
	}
	if d.RelayPulseGapMs, err = s.adminDeps.Settings.GetRelayPulseGapMs(ctx); err != nil {
		return d, err
	}
	if d.RelayMaxAttempts, err = s.adminDeps.Settings.GetRelayMaxAttempts(ctx); err != nil {
		return d, err
	}
	if d.DirectPayMaxCreditsPerTx, err = s.adminDeps.Settings.GetDirectPayMaxCreditsPerTx(ctx); err != nil {
		return d, err
	}
	if d.DirectPayRotateIntervalHr, err = s.adminDeps.Settings.GetDirectPayRotateIntervalHours(ctx); err != nil {
		return d, err
	}
	if d.DirectPayRotateAfterUses, err = s.adminDeps.Settings.GetDirectPayRotateAfterUses(ctx); err != nil {
		return d, err
	}
	return d, nil
}

func (s *Server) handleAdminSettingsShow(w http.ResponseWriter, r *http.Request) {
	data, err := s.loadAdminSettingsData(r.Context())
	if err != nil {
		s.logger.Error("failed to load settings", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data.User = adminUser(r)
	s.renderPage(w, "admin_settings.html", data)
}

func formInt(r *http.Request, name string) (int, error) {
	return strconv.Atoi(r.FormValue(name))
}

func formInt64(r *http.Request, name string) (int64, error) {
	return strconv.ParseInt(r.FormValue(name), 10, 64)
}

func (s *Server) handleAdminSettingsSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	minConf, err1 := formInt(r, "min_confirmations")
	zeroConfMax, err2 := formInt64(r, "zero_conf_max_koinu")
	warnThreshold, err3 := formInt(r, "pool_warn_threshold")
	urgentThreshold, err4 := formInt(r, "pool_urgent_threshold")
	tokenPrice, err5 := formInt64(r, "token_price_koinu")
	pulseGap, err6 := formInt(r, "relay_pulse_gap_ms")
	maxAttempts, err7 := formInt(r, "relay_max_attempts")
	directPayMaxCredits, err8 := formInt(r, "direct_pay_max_credits_per_tx")
	directPayRotateHours, err9 := formInt(r, "direct_pay_rotate_interval_hours")
	directPayRotateUses, err10 := formInt(r, "direct_pay_rotate_after_uses")

	var formErr string
	switch {
	case err1 != nil || minConf < 0:
		formErr = "min_confirmations must be a non-negative integer"
	case err2 != nil || zeroConfMax < 0:
		formErr = "zero_conf_max_koinu must be a non-negative integer"
	case err3 != nil || warnThreshold < 0:
		formErr = "pool_warn_threshold must be a non-negative integer"
	case err4 != nil || urgentThreshold < 0:
		formErr = "pool_urgent_threshold must be a non-negative integer"
	case err5 != nil || tokenPrice <= 0:
		formErr = "token_price_koinu must be a positive integer"
	case err6 != nil || pulseGap < 0:
		formErr = "relay_pulse_gap_ms must be a non-negative integer"
	case err7 != nil || maxAttempts <= 0:
		formErr = "relay_max_attempts must be a positive integer"
	case err8 != nil || directPayMaxCredits <= 0:
		formErr = "direct_pay_max_credits_per_tx must be a positive integer"
	case err9 != nil || directPayRotateHours < 0:
		formErr = "direct_pay_rotate_interval_hours must be a non-negative integer (0 disables)"
	case err10 != nil || directPayRotateUses < 0:
		formErr = "direct_pay_rotate_after_uses must be a non-negative integer (0 disables)"
	}
	if formErr != "" {
		data, err := s.loadAdminSettingsData(r.Context())
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		data.User = adminUser(r)
		data.Error = formErr
		s.renderPage(w, "admin_settings.html", data)
		return
	}

	ctx := r.Context()
	setters := []func() error{
		func() error { return s.adminDeps.Settings.SetMinConfirmations(ctx, minConf) },
		func() error { return s.adminDeps.Settings.SetZeroConfMaxKoinu(ctx, zeroConfMax) },
		func() error { return s.adminDeps.Settings.SetPoolWarnThreshold(ctx, warnThreshold) },
		func() error { return s.adminDeps.Settings.SetPoolUrgentThreshold(ctx, urgentThreshold) },
		func() error { return s.adminDeps.Settings.SetTokenPriceKoinu(ctx, tokenPrice) },
		func() error { return s.adminDeps.Settings.SetRelayPulseGapMs(ctx, pulseGap) },
		func() error { return s.adminDeps.Settings.SetRelayMaxAttempts(ctx, maxAttempts) },
		func() error { return s.adminDeps.Settings.SetDirectPayMaxCreditsPerTx(ctx, directPayMaxCredits) },
		func() error { return s.adminDeps.Settings.SetDirectPayRotateIntervalHours(ctx, directPayRotateHours) },
		func() error { return s.adminDeps.Settings.SetDirectPayRotateAfterUses(ctx, directPayRotateUses) },
	}
	for _, set := range setters {
		if err := set(); err != nil {
			s.logger.Error("failed to save settings", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}
	s.logAdminAction(r, "settings.update", "", "updated business settings")

	data, err := s.loadAdminSettingsData(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data.User = adminUser(r)
	data.Saved = true
	s.renderPage(w, "admin_settings.html", data)
}

// --- Node settings (7.3) ---

type adminNodeSettingsData struct {
	User *services.User
	// Config's RPCPass is always blanked out before rendering (see
	// redactNodeConfig) — the secret is never echoed back into the page,
	// even into a password input's value, since that puts it in the page
	// source on every load. HasRPCPass tells the template whether a
	// password is currently set, so it can say so without revealing it.
	//
	// Known tradeoff (deliberate, not a bug): since blank always means
	// "leave the password as-is" (see nodeConfigFromForm), there is no
	// form control to *clear* an already-set password short of editing the
	// settings table directly. Removing an RPC password entirely is rare
	// enough in practice (most nodes are configured once) that this was
	// judged not worth the extra "clear password" checkbox/affordance;
	// revisit if that assumption stops holding.
	Config      services.NodeRPCConfig
	HasRPCPass  bool
	Saved       bool
	CheckResult string
	CheckError  string
}

// redactNodeConfig strips the RPC password out of cfg before it's handed to
// a template, returning whether a password was set.
func redactNodeConfig(cfg services.NodeRPCConfig) (services.NodeRPCConfig, bool) {
	hasPass := cfg.RPCPass != ""
	cfg.RPCPass = ""
	return cfg, hasPass
}

// nodeConfigFromForm builds a NodeRPCConfig from the submitted form fields.
// An empty rpc_pass field means "leave the password as-is" rather than
// "clear it" — the field is never pre-filled with the real password (see
// redactNodeConfig), so blank is the only way to submit the form without
// touching the password. Shared by both handleAdminNodeSettingsSave and
// handleAdminNodeCheck so "Check connection" tests whatever is currently in
// the form (including an as-yet-unsaved edit), not stale values already on
// disk — the two actions submit the same fields from the same form.
func (s *Server) nodeConfigFromForm(ctx context.Context, r *http.Request) (services.NodeRPCConfig, error) {
	existing, err := s.adminDeps.Settings.GetNodeRPCConfig(ctx)
	if err != nil {
		return services.NodeRPCConfig{}, err
	}
	rpcPass := r.FormValue("rpc_pass")
	if rpcPass == "" {
		rpcPass = existing.RPCPass
	}
	return services.NodeRPCConfig{
		RPCURL:  r.FormValue("rpc_url"),
		RPCUser: r.FormValue("rpc_user"),
		RPCPass: rpcPass,
		ZMQAddr: r.FormValue("zmq_addr"),
	}, nil
}

func (s *Server) handleAdminNodeSettingsShow(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.adminDeps.Settings.GetNodeRPCConfig(r.Context())
	if err != nil {
		s.logger.Error("failed to load node settings", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	redacted, hasPass := redactNodeConfig(cfg)
	s.renderPage(w, "admin_node.html", adminNodeSettingsData{User: adminUser(r), Config: redacted, HasRPCPass: hasPass})
}

func (s *Server) handleAdminNodeSettingsSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	cfg, err := s.nodeConfigFromForm(r.Context(), r)
	if err != nil {
		s.logger.Error("failed to load existing node settings", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := s.adminDeps.Settings.SetNodeRPCConfig(r.Context(), cfg); err != nil {
		s.logger.Error("failed to save node settings", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.logAdminAction(r, "settings.node.update", "", "updated node RPC/ZMQ connection settings")
	redacted, hasPass := redactNodeConfig(cfg)
	s.renderPage(w, "admin_node.html", adminNodeSettingsData{User: adminUser(r), Config: redacted, HasRPCPass: hasPass, Saved: true})
}

func (s *Server) handleAdminNodeCheck(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	// Tests whatever is currently in the form (the same fields as the Save
	// button, via a shared <form> with formaction — see admin_node.html),
	// not just the last-saved config: an admin editing the RPC URL before
	// saving expects "Check connection" to test the edit, not stale values
	// already on disk.
	cfg, err := s.nodeConfigFromForm(r.Context(), r)
	if err != nil {
		s.logger.Error("failed to load node settings", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	redacted, hasPass := redactNodeConfig(cfg)
	data := adminNodeSettingsData{User: adminUser(r), Config: redacted, HasRPCPass: hasPass}
	client, err := corerpc.NewClient(cfg.RPCURL, cfg.RPCUser, cfg.RPCPass)
	if err != nil {
		data.CheckError = err.Error()
	} else {
		checkCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		info, err := client.GetBlockchainInfo(checkCtx)
		cancel()
		if err != nil {
			data.CheckError = err.Error()
		} else {
			data.CheckResult = fmt.Sprintf("Connected: chain=%s, blocks=%d", info.Chain, info.Blocks)
		}
	}
	s.renderPage(w, "admin_node.html", data)
}

// --- Machines + relays (7.4) ---

type adminMachinesData struct {
	User      *services.User
	Machines  []services.Machine
	Boards    []services.RelayBoard
	Bindings  []services.MachineRelay
	DirectPay map[int64]services.DirectPayAddress
	// MachineURLs and MachineQR key by machine ID: the absolute /m/{slug}
	// page URL and a small QR thumbnail rendering it, so a freshly
	// registered machine has a scannable code right on this page instead
	// of the operator having to build the URL and paste it into some
	// third-party QR generator by hand.
	MachineURLs map[int64]string
	MachineQR   map[int64]template.URL
	Error       string
}

func (s *Server) machinePageURL(slug string) string {
	return strings.TrimRight(s.adminDeps.BaseURL, "/") + "/m/" + slug
}

func (s *Server) loadAdminMachinesData(ctx context.Context) (adminMachinesData, error) {
	var d adminMachinesData
	var err error
	if d.Machines, err = s.adminDeps.Machines.ListAll(ctx); err != nil {
		return d, err
	}
	if d.Boards, err = s.adminDeps.Relays.ListBoards(ctx); err != nil {
		return d, err
	}
	if d.Bindings, err = s.adminDeps.Relays.ListBindings(ctx); err != nil {
		return d, err
	}
	d.DirectPay = make(map[int64]services.DirectPayAddress)
	d.MachineURLs = make(map[int64]string)
	d.MachineQR = make(map[int64]template.URL)
	for _, m := range d.Machines {
		if m.DirectPayEnabled {
			active, ok, err := s.adminDeps.DirectPay.ActiveAddress(ctx, m.ID)
			if err != nil {
				return d, err
			}
			if ok {
				d.DirectPay[m.ID] = active
			}
		}

		pageURL := s.machinePageURL(m.Slug)
		d.MachineURLs[m.ID] = pageURL
		if qr, err := qrDataURI(pageURL, 120); err != nil {
			s.logger.Error("failed to render machine QR thumbnail", "machine_id", m.ID, "err", err)
		} else {
			d.MachineQR[m.ID] = template.URL(qr)
		}
	}
	return d, nil
}

func (s *Server) handleAdminMachinesShow(w http.ResponseWriter, r *http.Request) {
	data, err := s.loadAdminMachinesData(r.Context())
	if err != nil {
		s.logger.Error("failed to load machines admin data", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data.User = adminUser(r)
	s.renderPage(w, "admin_machines.html", data)
}

func (s *Server) handleAdminMachineCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	slug := r.FormValue("slug")
	name := r.FormValue("name")
	id, err := s.adminDeps.Machines.Create(r.Context(), slug, name)
	if err != nil {
		data, loadErr := s.loadAdminMachinesData(r.Context())
		if loadErr != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		data.User = adminUser(r)
		if errors.Is(err, services.ErrMachineSlugTaken) {
			data.Error = "That machine slug is already in use."
		} else {
			s.logger.Error("failed to create machine", "err", err)
			data.Error = "Failed to create machine."
		}
		s.renderPage(w, "admin_machines.html", data)
		return
	}
	s.logAdminAction(r, "machine.create", fmt.Sprintf("machine:%d", id), fmt.Sprintf("slug=%s name=%s", slug, name))
	s.adminRedirect(w, r, "/admin/machines")
}

func (s *Server) handleAdminMachineEdit(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.Error(w, "invalid machine id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	slug := r.FormValue("slug")
	name := r.FormValue("name")
	if err := s.adminDeps.Machines.Update(r.Context(), id, slug, name); err != nil {
		data, loadErr := s.loadAdminMachinesData(r.Context())
		if loadErr != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		data.User = adminUser(r)
		if errors.Is(err, services.ErrMachineSlugTaken) {
			data.Error = "That machine slug is already in use."
		} else {
			s.logger.Error("failed to update machine", "id", id, "err", err)
			data.Error = "Failed to update machine."
		}
		s.renderPage(w, "admin_machines.html", data)
		return
	}
	s.logAdminAction(r, "machine.edit", fmt.Sprintf("machine:%d", id), fmt.Sprintf("slug=%s name=%s", slug, name))
	s.adminRedirect(w, r, "/admin/machines")
}

// handleAdminMachineQR renders a standalone, print-friendly page with a
// large QR code for a machine's customer-facing /m/{slug} page: scan it to
// pull up the redeem/direct-pay page for that specific cabinet. Meant to be
// printed and taped to (or laminated onto) the machine itself.
type adminMachineQRData struct {
	User    *services.User
	Machine services.Machine
	URL     string
	QR      template.URL
}

func (s *Server) handleAdminMachineQR(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.Error(w, "invalid machine id", http.StatusBadRequest)
		return
	}
	machine, err := s.adminDeps.Machines.GetByID(r.Context(), id)
	if errors.Is(err, services.ErrMachineSlugNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.logger.Error("failed to look up machine", "id", id, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	pageURL := s.machinePageURL(machine.Slug)
	qr, err := qrDataURIWithRecovery(pageURL, qrcode.High, 480)
	if err != nil {
		s.logger.Error("failed to render machine QR code", "machine_id", id, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	s.renderPage(w, "admin_machine_qr.html", adminMachineQRData{
		User:    adminUser(r),
		Machine: machine,
		URL:     pageURL,
		QR:      template.URL(qr),
	})
}

func (s *Server) handleAdminMachineToggle(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.Error(w, "invalid machine id", http.StatusBadRequest)
		return
	}
	active := r.FormValue("active") == "1"
	if err := s.adminDeps.Machines.SetActive(r.Context(), id, active); err != nil {
		s.logger.Error("failed to toggle machine", "id", id, "err", err)
	} else {
		s.logAdminAction(r, "machine.set_active", fmt.Sprintf("machine:%d", id), fmt.Sprintf("active=%v", active))
	}
	s.adminRedirect(w, r, "/admin/machines")
}

func (s *Server) handleAdminBoardCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	name := r.FormValue("name")
	baseURL := r.FormValue("base_url")
	id, err := s.adminDeps.Relays.CreateBoard(r.Context(), name, baseURL)
	if err != nil {
		data, loadErr := s.loadAdminMachinesData(r.Context())
		if loadErr != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		data.User = adminUser(r)
		if errors.Is(err, services.ErrBoardNameTaken) {
			data.Error = "That relay board name is already in use."
		} else {
			s.logger.Error("failed to create relay board", "err", err)
			data.Error = "Failed to create relay board."
		}
		s.renderPage(w, "admin_machines.html", data)
		return
	}
	s.logAdminAction(r, "board.create", fmt.Sprintf("board:%d", id), fmt.Sprintf("name=%s base_url=%s", name, baseURL))
	s.adminRedirect(w, r, "/admin/machines")
}

func (s *Server) handleAdminBoardToggle(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.Error(w, "invalid board id", http.StatusBadRequest)
		return
	}
	active := r.FormValue("active") == "1"
	if err := s.adminDeps.Relays.SetBoardActive(r.Context(), id, active); err != nil {
		s.logger.Error("failed to toggle relay board", "id", id, "err", err)
	} else {
		s.logAdminAction(r, "board.set_active", fmt.Sprintf("board:%d", id), fmt.Sprintf("active=%v", active))
	}
	s.adminRedirect(w, r, "/admin/machines")
}

func (s *Server) handleAdminRelayBind(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	machineID, err1 := formInt64(r, "machine_id")
	boardID, err2 := formInt64(r, "board_id")
	relayNumber, err3 := formInt(r, "relay_number")
	if err1 != nil || err2 != nil || err3 != nil {
		http.Error(w, "invalid form values", http.StatusBadRequest)
		return
	}
	id, err := s.adminDeps.Relays.Bind(r.Context(), machineID, boardID, relayNumber)
	if err != nil {
		data, loadErr := s.loadAdminMachinesData(r.Context())
		if loadErr != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		data.User = adminUser(r)
		if errors.Is(err, services.ErrBindingConflict) {
			data.Error = "That machine already has an active relay binding. Unbind it first."
		} else {
			s.logger.Error("failed to bind relay", "err", err)
			data.Error = "Failed to bind relay."
		}
		s.renderPage(w, "admin_machines.html", data)
		return
	}
	s.logAdminAction(r, "relay.bind", fmt.Sprintf("binding:%d", id), fmt.Sprintf("machine=%d board=%d relay=%d", machineID, boardID, relayNumber))
	s.adminRedirect(w, r, "/admin/machines")
}

func (s *Server) handleAdminRelayUnbind(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.Error(w, "invalid binding id", http.StatusBadRequest)
		return
	}
	if err := s.adminDeps.Relays.Unbind(r.Context(), id); err != nil {
		s.logger.Error("failed to unbind relay", "id", id, "err", err)
	} else {
		s.logAdminAction(r, "relay.unbind", fmt.Sprintf("binding:%d", id), "")
	}
	s.adminRedirect(w, r, "/admin/machines")
}

func (s *Server) handleAdminTestFire(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	machineID, err := formInt64(r, "machine_id")
	if err != nil {
		http.Error(w, "invalid machine id", http.StatusBadRequest)
		return
	}
	testFireErr := s.adminDeps.Dispatcher.TestFire(r.Context(), machineID)

	data, loadErr := s.loadAdminMachinesData(r.Context())
	if loadErr != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data.User = adminUser(r)
	if testFireErr != nil {
		data.Error = "Test-fire failed: " + testFireErr.Error()
		s.logAdminAction(r, "relay.test_fire", fmt.Sprintf("machine:%d", machineID), "failed: "+testFireErr.Error())
	} else {
		s.logAdminAction(r, "relay.test_fire", fmt.Sprintf("machine:%d", machineID), "ok")
	}
	s.renderPage(w, "admin_machines.html", data)
}

func (s *Server) handleAdminMachineDirectPaySave(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.Error(w, "invalid machine id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	enabled := r.FormValue("enabled") == "1"
	priceKoinu, priceErr := formInt64(r, "price_koinu")

	renderErr := func(msg string) {
		data, loadErr := s.loadAdminMachinesData(r.Context())
		if loadErr != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		data.User = adminUser(r)
		data.Error = msg
		s.renderPage(w, "admin_machines.html", data)
	}

	if enabled && (priceErr != nil || priceKoinu <= 0) {
		renderErr("Direct-pay price must be a positive number of koinu.")
		return
	}

	if err := s.adminDeps.Machines.SetDirectPay(r.Context(), id, enabled, priceKoinu); err != nil {
		s.logger.Error("failed to set direct pay", "machine_id", id, "err", err)
		renderErr("Failed to save direct-pay settings.")
		return
	}
	s.logAdminAction(r, "machine.direct_pay.set", fmt.Sprintf("machine:%d", id), fmt.Sprintf("enabled=%v price_koinu=%d", enabled, priceKoinu))

	if enabled {
		if _, ok, err := s.adminDeps.DirectPay.ActiveAddress(r.Context(), id); err != nil {
			s.logger.Error("failed to check active direct-pay address", "machine_id", id, "err", err)
		} else if !ok {
			if _, err := s.adminDeps.DirectPay.Activate(r.Context(), id); err != nil {
				if errors.Is(err, services.ErrPoolEmpty) {
					renderErr("Direct pay enabled, but the machine_direct address pool is empty — import more addresses to activate it.")
					return
				}
				s.logger.Error("failed to activate direct-pay address", "machine_id", id, "err", err)
				renderErr("Failed to activate a direct-pay address.")
				return
			}
			s.logAdminAction(r, "machine.direct_pay.activate", fmt.Sprintf("machine:%d", id), "")
		}
	}

	s.adminRedirect(w, r, "/admin/machines")
}

func (s *Server) handleAdminMachineDirectPayRotate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.Error(w, "invalid machine id", http.StatusBadRequest)
		return
	}
	_, rotateErr := s.adminDeps.DirectPay.Rotate(r.Context(), id)
	switch {
	case rotateErr == nil:
		s.logAdminAction(r, "machine.direct_pay.rotate", fmt.Sprintf("machine:%d", id), "ok")
	case errors.Is(rotateErr, services.ErrPoolEmpty):
		s.logAdminAction(r, "machine.direct_pay.rotate", fmt.Sprintf("machine:%d", id), "failed: pool empty")
	default:
		s.logger.Error("failed to rotate direct-pay address", "machine_id", id, "err", rotateErr)
		s.logAdminAction(r, "machine.direct_pay.rotate", fmt.Sprintf("machine:%d", id), "failed: "+rotateErr.Error())
	}
	s.adminRedirect(w, r, "/admin/machines")
}

// --- Addresses (7.5) ---

type adminAddressesData struct {
	User    *services.User
	Counts  map[string]int
	Pool    []services.Address
	Retired []services.Address
	Batches []services.AddressBatch
	Error   string
	Message string
}

func (s *Server) loadAdminAddressesData(ctx context.Context) (adminAddressesData, error) {
	var d adminAddressesData
	var err error
	if d.Counts, err = s.adminDeps.Pool.CountsByState(ctx); err != nil {
		return d, err
	}
	if d.Pool, err = s.adminDeps.Pool.ListByState(ctx, "pool", 100); err != nil {
		return d, err
	}
	if d.Retired, err = s.adminDeps.Pool.ListByState(ctx, "retired", 50); err != nil {
		return d, err
	}
	if d.Batches, err = s.adminDeps.Pool.ListBatches(ctx); err != nil {
		return d, err
	}
	return d, nil
}

func (s *Server) handleAdminAddressesShow(w http.ResponseWriter, r *http.Request) {
	data, err := s.loadAdminAddressesData(r.Context())
	if err != nil {
		s.logger.Error("failed to load addresses admin data", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data.User = adminUser(r)
	s.renderPage(w, "admin_addresses.html", data)
}

func (s *Server) handleAdminAddressesImport(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	addrs := splitLines(r.FormValue("addresses"))
	purpose := r.FormValue("purpose")
	if purpose == "" {
		purpose = "token_deposit"
	}

	renderErr := func(msg string) {
		data, loadErr := s.loadAdminAddressesData(r.Context())
		if loadErr != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		data.User = adminUser(r)
		data.Error = msg
		s.renderPage(w, "admin_addresses.html", data)
	}

	if len(addrs) == 0 {
		renderErr("Paste at least one address (one per line).")
		return
	}
	if purpose != "token_deposit" && purpose != "machine_direct" {
		renderErr("Invalid purpose.")
		return
	}

	batchID, err := s.adminDeps.AddressBatches.ImportBatch(r.Context(), "admin console upload", addrs, s.adminDeps.NodeImporter, purpose)
	if err != nil {
		renderErr("Import failed: " + err.Error())
		return
	}
	s.logAdminAction(r, "addresses.import", fmt.Sprintf("batch:%d", batchID), fmt.Sprintf("%d addresses", len(addrs)))
	s.adminRedirect(w, r, "/admin/addresses")
}

func (s *Server) handleAdminAddressRetire(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.Error(w, "invalid address id", http.StatusBadRequest)
		return
	}
	if err := s.adminDeps.Pool.Retire(r.Context(), id); err != nil {
		s.logger.Error("failed to retire address", "id", id, "err", err)
	} else {
		s.logAdminAction(r, "address.retire", fmt.Sprintf("address:%d", id), "")
	}
	s.adminRedirect(w, r, "/admin/addresses")
}

// --- Deposits (7.6) ---

type adminDepositsData struct {
	User        *services.User
	Deposits    []services.Deposit
	StateFilter string
}

const adminDepositsLimit = 200

func (s *Server) handleAdminDepositsShow(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	deposits, err := s.adminDeps.Deposits.List(r.Context(), state, adminDepositsLimit)
	if err != nil {
		s.logger.Error("failed to list deposits", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderPage(w, "admin_deposits.html", adminDepositsData{User: adminUser(r), Deposits: deposits, StateFilter: state})
}

// --- Users (7.6) ---

type adminUsersData struct {
	User  *services.User
	Query string
	Users []services.User
}

const adminUsersLimit = 50

func (s *Server) handleAdminUsersShow(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	var users []services.User
	if query != "" {
		var err error
		users, err = s.adminDeps.Users.SearchByDisplayName(r.Context(), query, adminUsersLimit)
		if err != nil {
			s.logger.Error("failed to search users", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}
	s.renderPage(w, "admin_users.html", adminUsersData{User: adminUser(r), Query: query, Users: users})
}

type adminUserDetailData struct {
	User        *services.User
	Target      services.User
	Balance     int64
	History     []services.LedgerEntry
	Error       string
	AdjustNote  string
	AdjustDelta string
}

const adminUserHistoryLimit = 100

func (s *Server) loadAdminUserDetailData(ctx context.Context, id int64) (adminUserDetailData, error) {
	var d adminUserDetailData
	var err error
	if d.Target, err = s.adminDeps.Users.GetByID(ctx, id); err != nil {
		return d, err
	}
	if d.Balance, err = s.adminDeps.Ledger.Balance(ctx, id); err != nil {
		return d, err
	}
	if d.History, err = s.adminDeps.Ledger.History(ctx, id, adminUserHistoryLimit); err != nil {
		return d, err
	}
	return d, nil
}

func (s *Server) handleAdminUserShow(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.Error(w, "invalid user id", http.StatusBadRequest)
		return
	}
	data, err := s.loadAdminUserDetailData(r.Context(), id)
	if err != nil {
		s.logger.Error("failed to load user detail", "id", id, "err", err)
		http.NotFound(w, r)
		return
	}
	data.User = adminUser(r)
	s.renderPage(w, "admin_user.html", data)
}

func (s *Server) handleAdminUserAdjust(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.Error(w, "invalid user id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	delta, err := formInt64(r, "delta")
	note := r.FormValue("note")

	renderErr := func(msg string) {
		data, loadErr := s.loadAdminUserDetailData(r.Context(), id)
		if loadErr != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		data.User = adminUser(r)
		data.Error = msg
		s.renderPage(w, "admin_user.html", data)
	}

	if err != nil {
		renderErr("Delta must be an integer (positive to credit, negative to debit).")
		return
	}
	if note == "" {
		renderErr("A note is required for manual balance adjustments.")
		return
	}

	if err := s.adminDeps.Ledger.AdminAdjust(r.Context(), id, delta, note); err != nil {
		s.logger.Error("failed to apply admin adjustment", "user_id", id, "err", err)
		renderErr("Failed to apply adjustment: " + err.Error())
		return
	}
	s.logAdminAction(r, "user.adjust", fmt.Sprintf("user:%d", id), fmt.Sprintf("delta=%d note=%s", delta, note))
	s.adminRedirect(w, r, fmt.Sprintf("/admin/users/%d", id))
}

func (s *Server) handleAdminUserMerge(w http.ResponseWriter, r *http.Request) {
	toID, err := pathID(r)
	if err != nil {
		http.Error(w, "invalid user id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	fromID, err := formInt64(r, "from_id")

	renderErr := func(msg string) {
		data, loadErr := s.loadAdminUserDetailData(r.Context(), toID)
		if loadErr != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		data.User = adminUser(r)
		data.Error = msg
		s.renderPage(w, "admin_user.html", data)
	}

	if err != nil {
		renderErr("Enter the numeric ID of the account to merge from.")
		return
	}
	if err := s.adminDeps.Users.Merge(r.Context(), fromID, toID); err != nil {
		s.logger.Error("failed to merge users", "from", fromID, "to", toID, "err", err)
		renderErr("Merge failed: " + err.Error())
		return
	}
	s.logAdminAction(r, "user.merge", fmt.Sprintf("user:%d", toID), fmt.Sprintf("merged from user:%d", fromID))
	s.adminRedirect(w, r, fmt.Sprintf("/admin/users/%d", toID))
}

// splitLines splits a textarea's contents into addresses, one per line,
// skipping blank lines and "#"-prefixed comments (mirroring the
// `dogecade addresses import` file format).
func splitLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}
