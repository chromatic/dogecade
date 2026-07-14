package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/chromatic/dogecade/internal/auth"
	"github.com/chromatic/dogecade/internal/chain"
	"github.com/chromatic/dogecade/internal/chain/corerpc"
	"github.com/chromatic/dogecade/internal/config"
	"github.com/chromatic/dogecade/internal/relay"
	"github.com/chromatic/dogecade/internal/services"
	"github.com/chromatic/dogecade/internal/store"
	"github.com/chromatic/dogecade/internal/web"
)

// nodeHealthAdapter adapts chain.NodeHealthChecker's typed NodeState return
// to the plain-string return web.Server's health-checker interface expects
// (an interface method signature must match exactly; NodeState's underlying
// type being string isn't enough).
type nodeHealthAdapter struct {
	checker *chain.NodeHealthChecker
}

func (a nodeHealthAdapter) Check(ctx context.Context) (string, error) {
	state, err := a.checker.Check(ctx)
	return string(state), err
}

// version is set at build time via ldflags
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: dogecade <subcommand> [options]\n")
		fmt.Fprintf(os.Stderr, "Subcommands: version, serve, addresses, relays, users, machines\n")
		os.Exit(1)
	}

	subcommand := os.Args[1]
	args := os.Args[2:]

	ctx := context.Background()

	switch subcommand {
	case "version":
		if err := cmdVersion(ctx, args); err != nil {
			fmt.Fprintf(os.Stderr, "version: %v\n", err)
			os.Exit(1)
		}
	case "serve":
		if err := cmdServe(ctx, args); err != nil {
			fmt.Fprintf(os.Stderr, "serve: %v\n", err)
			os.Exit(1)
		}
	case "addresses":
		if err := cmdAddresses(ctx, args); err != nil {
			fmt.Fprintf(os.Stderr, "addresses: %v\n", err)
			os.Exit(1)
		}
	case "relays":
		if err := cmdRelays(ctx, args); err != nil {
			fmt.Fprintf(os.Stderr, "relays: %v\n", err)
			os.Exit(1)
		}
	case "users":
		if err := cmdUsers(ctx, args); err != nil {
			fmt.Fprintf(os.Stderr, "users: %v\n", err)
			os.Exit(1)
		}
	case "machines":
		if err := cmdMachines(ctx, args); err != nil {
			fmt.Fprintf(os.Stderr, "machines: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "Unknown subcommand: %s\n", subcommand)
		fmt.Fprintf(os.Stderr, "Available subcommands: version, serve, addresses, relays, users, machines\n")
		os.Exit(1)
	}
}

func cmdVersion(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("version", flag.ExitOnError)
	_ = fs.Parse(args)

	fmt.Println(version)
	return nil
}

func cmdServe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	_ = fs.Parse(args)

	// Create structured logger (JSON for services)
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	// Load configuration from environment
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		logger.Error("configuration error", "error", err)
		return fmt.Errorf("configuration error: %w", err)
	}

	// Log loaded configuration (redact sensitive values)
	logger.Info("Configuration loaded",
		"db_path", cfg.DBPath,
		"base_url", cfg.BaseURL,
		"listen_addr", cfg.ListenAddr,
	)

	// Open the database store
	s, err := store.Open(cfg.DBPath)
	if err != nil {
		logger.Error("failed to open database store", "error", err)
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer func() { _ = s.Close() }()

	logger.Info("Database connected")

	// Seed settings from environment
	settingsSvc := services.NewSettingsService(s)
	err = settingsSvc.SeedFromEnv(ctx, cfg)
	if err != nil {
		logger.Error("failed to seed settings from environment", "error", err)
		return fmt.Errorf("failed to seed settings: %w", err)
	}

	logger.Info("Settings initialized from environment")

	// Set up graceful shutdown with signal handling. Background workers
	// (chain watcher, relay dispatcher, board health poller) all run until
	// sigCtx is cancelled.
	sigCtx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Optional Dogecoin node RPC client; nil (ErrNotConfigured) is a
	// first-class "unconfigured" state, not a startup error.
	var nodeClient *corerpc.Client
	if cfg.DogecoinRPCURL != "" {
		client, err := corerpc.NewClient(cfg.DogecoinRPCURL, cfg.DogecoinRPCUser, cfg.DogecoinRPCPass)
		if err != nil && err != corerpc.ErrNotConfigured {
			return fmt.Errorf("failed to create node RPC client: %w", err)
		}
		nodeClient = client
	}
	nodeHealthChecker := chain.NewNodeHealthChecker(nodeClient)

	poolSvc := services.NewPoolService(s, settingsSvc)
	ledgerSvc := services.NewLedgerService(s)
	usersSvc := services.NewUsersService(s)
	purchaseSvc := services.NewPurchaseService(s)
	redemptionSvc := services.NewRedemptionService(s, ledgerSvc)
	machinesSvc := services.NewMachinesService(s)
	addressBatchSvc := services.NewAddressBatchService(s)
	relaysSvc := services.NewRelaysService(s)
	depositsSvc := services.NewDepositsService(s)
	alertsSvc := services.NewAlertsService(s)
	adminAuditSvc := services.NewAdminAuditService(s)
	directPaySvc := services.NewDirectPayService(s, settingsSvc)

	// nodeImporter is left as a nil services.NodeImporter (rather than a
	// non-nil interface wrapping a nil *corerpc.Client) when the node isn't
	// configured, so admin address imports degrade the same way `dogecade
	// addresses import` does: addresses land pending node registration.
	var nodeImporter services.NodeImporter
	if nodeClient != nil {
		nodeImporter = nodeClient
	}

	// Chain watcher + deposit pipeline: turns confirmed payments into
	// credited token balances. Degrades gracefully to idle when the node
	// isn't configured (CoreWatcher.Run just waits on ctx.Done()).
	watcher := chain.NewCoreWatcher(nodeClient, settingsSvc)
	creditHook := services.NewDirectPayAwareCreditHook(s, settingsSvc, ledgerSvc, directPaySvc)
	depositPipeline := services.NewDepositPipeline(s, settingsSvc, poolSvc, creditHook)

	go watcher.Run(sigCtx)
	go func() {
		for {
			select {
			case <-sigCtx.Done():
				return
			case ev := <-watcher.Notifications():
				if err := depositPipeline.HandleEvent(sigCtx, ev.Address, ev.TxID, ev.Vout, ev.AmountKoinu, ev.Confirmations, ev.BlockHeight); err != nil {
					logger.Error("failed to handle payment event", "txid", ev.TxID, "err", err)
				}
			case ev := <-watcher.RemovedNotifications():
				if err := depositPipeline.HandleReorg(sigCtx, ev.TxID, ev.Vout); err != nil {
					logger.Error("failed to handle reorg event", "txid", ev.TxID, "err", err)
				}
			}
		}
	}()

	if cfg.DogecoinZMQAddr != "" {
		nudger := chain.NewZMQNudger(cfg.DogecoinZMQAddr, watcher.TriggerPoll)
		go func() {
			if err := nudger.Run(sigCtx); err != nil {
				logger.Error("zmq nudger exited with error", "err", err)
			}
		}()
	}

	// Relay dispatcher + board health poller (Phase 5): pending credit
	// pulses become Tasmota HTTP calls, with refund-on-failure.
	dispatcher := relay.NewDispatcher(s, ledgerSvc, settingsSvc)
	go dispatcher.Run(sigCtx)
	boardHealth := relay.NewBoardHealthChecker(s)
	go boardHealth.Run(sigCtx)

	// Direct-pay address rotation (Phase 8): retires + replaces each
	// direct-pay-enabled machine's active address on a configurable
	// interval/use-count schedule. A no-op loop when both are left at 0.
	rotationJob := services.NewRotationJob(s, settingsSvc, directPaySvc, logger)
	go rotationJob.Run(sigCtx)

	// OIDC (Phase 6): all three DOGECADE_OIDC_* vars must be set together
	// (validated in config.Load); unset means sign-in is unavailable but the
	// rest of the server still runs.
	var provider *auth.Provider
	if cfg.OIDCConfigured() {
		discoverCtx, discoverCancel := context.WithTimeout(ctx, 10*time.Second)
		provider, err = auth.NewProvider(discoverCtx, cfg.OIDCIssuerURL, cfg.OIDCClientID, cfg.OIDCClientSecret, strings.TrimRight(cfg.BaseURL, "/")+"/auth/callback")
		discoverCancel()
		if err != nil {
			return fmt.Errorf("failed to initialize OIDC provider: %w", err)
		}
		logger.Info("OIDC sign-in configured", "issuer", cfg.OIDCIssuerURL)
	} else {
		logger.Warn("OIDC not configured (DOGECADE_OIDC_* unset); customer sign-in is unavailable")
	}

	secureCookies := strings.HasPrefix(cfg.BaseURL, "https://")
	sessionSecret := []byte(cfg.SessionSecret)
	if len(sessionSecret) == 0 {
		sessionSecret = make([]byte, 32)
		if _, err := rand.Read(sessionSecret); err != nil {
			return fmt.Errorf("failed to generate session secret: %w", err)
		}
		logger.Warn("DOGECADE_SESSION_SECRET not set; generated an ephemeral secret, so all sessions will be invalidated on restart")
	}
	sessions := auth.NewSessionManager(sessionSecret, secureCookies)
	adminSubjects := auth.ParseAdminSubjects(cfg.AdminSubjects)
	authHandlers := auth.NewHandlers(sessions, provider, usersSvc, adminSubjects, secureCookies)

	// Create HTTP server
	webServer := web.NewServer(s, logger, nodeHealthAdapter{checker: nodeHealthChecker})
	if err := webServer.RegisterCustomerRoutes(web.CustomerDeps{
		Auth:       authHandlers,
		Ledger:     ledgerSvc,
		Purchase:   purchaseSvc,
		Redemption: redemptionSvc,
		Machines:   machinesSvc,
		Settings:   settingsSvc,
		DirectPay:  directPaySvc,
	}); err != nil {
		return fmt.Errorf("failed to register customer routes: %w", err)
	}
	if err := webServer.RegisterAdminRoutes(web.AdminDeps{
		Auth:           authHandlers,
		Settings:       settingsSvc,
		Pool:           poolSvc,
		AddressBatches: addressBatchSvc,
		Machines:       machinesSvc,
		Relays:         relaysSvc,
		Deposits:       depositsSvc,
		Users:          usersSvc,
		Ledger:         ledgerSvc,
		Alerts:         alertsSvc,
		Audit:          adminAuditSvc,
		Dispatcher:     dispatcher,
		NodeImporter:   nodeImporter,
		DirectPay:      directPaySvc,
		BaseURL:        cfg.BaseURL,
	}); err != nil {
		return fmt.Errorf("failed to register admin routes: %w", err)
	}
	httpServer := &http.Server{
		Addr:           cfg.ListenAddr,
		Handler:        webServer.Handler(),
		ReadTimeout:    15 * time.Second,
		WriteTimeout:   15 * time.Second,
		IdleTimeout:    60 * time.Second,
		MaxHeaderBytes: 1 << 20, // 1 MB
	}

	logger.Info("HTTP server created", "listen_addr", cfg.ListenAddr)

	// Start server in a goroutine
	serverErr := make(chan error, 1)
	go func() {
		logger.Info("server starting", "addr", httpServer.Addr)
		err := httpServer.ListenAndServe()
		if err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
		serverErr <- nil
	}()

	// Wait for shutdown signal or server error
	select {
	case err := <-serverErr:
		if err != nil {
			logger.Error("server error", "error", err)
			return err
		}
	case <-sigCtx.Done():
		logger.Info("shutdown signal received", "signal", sigCtx.Err())

		// Graceful shutdown with timeout
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()

		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			logger.Error("graceful shutdown error", "error", err)
			return fmt.Errorf("shutdown error: %w", err)
		}

		logger.Info("server shutdown complete")
	}

	return nil
}

func cmdAddresses(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: dogecade addresses <subcommand> [options]\nSubcommands: import, generate")
	}

	subcommand := args[0]
	subargs := args[1:]

	switch subcommand {
	case "import":
		return cmdAddressesImport(ctx, subargs)
	case "generate":
		return cmdAddressesGenerate(ctx, subargs)
	default:
		return fmt.Errorf("unknown addresses subcommand: %s\nAvailable: import, generate", subcommand)
	}
}

func cmdAddressesImport(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("addresses import", flag.ExitOnError)
	purpose := fs.String("purpose", "token_deposit", "address purpose: token_deposit or machine_direct")
	_ = fs.Parse(args)

	if fs.NArg() < 1 {
		return fmt.Errorf("usage: dogecade addresses import [--purpose=token_deposit|machine_direct] <file>")
	}
	if *purpose != "token_deposit" && *purpose != "machine_direct" {
		return fmt.Errorf("invalid --purpose %q: must be token_deposit or machine_direct", *purpose)
	}

	filePath := fs.Arg(0)

	// Load configuration from environment
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return fmt.Errorf("configuration error: %w", err)
	}

	// Open the database store
	s, err := store.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer func() { _ = s.Close() }()

	// Optional: try to connect to the Dogecoin node if configured
	var node services.NodeImporter
	if cfg.DogecoinRPCURL != "" {
		nodeClient, err := corerpc.NewClient(cfg.DogecoinRPCURL, cfg.DogecoinRPCUser, cfg.DogecoinRPCPass)
		if err == corerpc.ErrNotConfigured {
			// Node not configured, continue with node=nil
			fmt.Fprintf(os.Stderr, "Warning: Dogecoin node not configured; addresses will be pending node registration\n")
		} else if err != nil {
			return fmt.Errorf("failed to create node client: %w", err)
		} else {
			node = nodeClient
		}
	} else {
		fmt.Fprintf(os.Stderr, "Note: Dogecoin node not configured; addresses will be pending node registration\n")
	}

	// Read addresses from file
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer func() { _ = file.Close() }()

	var addrs []string
	scanner := bufio.NewScanner(file)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		// Skip blank lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		addrs = append(addrs, line)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading file: %w", err)
	}

	if len(addrs) == 0 {
		return fmt.Errorf("no addresses found in file (only blank lines and comments)")
	}

	// Import the batch
	batchSvc := services.NewAddressBatchService(s)
	batchID, err := batchSvc.ImportBatch(ctx, filePath, addrs, node, *purpose)
	if err != nil {
		return fmt.Errorf("failed to import batch: %w", err)
	}

	// Query to determine how many addresses are registered with the node
	var registeredCount int
	err = s.DB().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM addresses WHERE batch_id = ? AND node_registered_at IS NOT NULL",
		batchID,
	).Scan(&registeredCount)
	if err != nil {
		return fmt.Errorf("failed to query registration status: %w", err)
	}

	pendingCount := len(addrs) - registeredCount

	// Print summary
	fmt.Printf("Import successful!\n")
	fmt.Printf("  Batch ID: %d\n", batchID)
	fmt.Printf("  Addresses imported: %d\n", len(addrs))
	fmt.Printf("  Registered with node: %d\n", registeredCount)
	fmt.Printf("  Pending node registration: %d\n", pendingCount)

	return nil
}

func cmdRelays(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: dogecade relays <subcommand> [options]\nSubcommands: test-fire, create-board, bind")
	}

	subcommand := args[0]
	subargs := args[1:]

	switch subcommand {
	case "test-fire":
		return cmdRelaysTestFire(ctx, subargs)
	case "create-board":
		return cmdRelaysCreateBoard(ctx, subargs)
	case "bind":
		return cmdRelaysBind(ctx, subargs)
	default:
		return fmt.Errorf("unknown relays subcommand: %s\nAvailable: test-fire, create-board, bind", subcommand)
	}
}

// cmdUsers dispatches `dogecade users` subcommands.
func cmdUsers(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: dogecade users <subcommand> [options]\nSubcommands: seed-admins")
	}

	subcommand := args[0]
	subargs := args[1:]

	switch subcommand {
	case "seed-admins":
		return cmdUsersSeedAdmins(ctx, subargs)
	default:
		return fmt.Errorf("unknown users subcommand: %s\nAvailable: seed-admins", subcommand)
	}
}

// cmdUsersSeedAdmins creates (or confirms) a user row with is_admin=1 for
// every issuer|subject pair in DOGECADE_ADMIN_SUBJECTS, instead of relying
// solely on isAdmin-at-first-login. Useful any time that env var changes on
// a deployment that already has users (adding an admin later still needs a
// real first login today, since GetOrCreateBySubjectHash only applies
// isAdmin at row creation) or when you want an admin account to exist
// before anyone signs in — e.g. so DOGECADE_ADMIN_SUBJECTS's exact issuer
// string is guaranteed to match this account regardless of how faithfully
// the identity provider's ID token echoes it back at login time (trailing
// slash, container-vs-public hostname, etc). Idempotent: re-running it is a
// no-op for subjects that already have a row.
func cmdUsersSeedAdmins(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("users seed-admins", flag.ExitOnError)
	_ = fs.Parse(args)

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return fmt.Errorf("configuration error: %w", err)
	}

	s, err := store.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer func() { _ = s.Close() }()

	adminSubjects := auth.ParseAdminSubjects(cfg.AdminSubjects)
	if len(adminSubjects) == 0 {
		fmt.Println("DOGECADE_ADMIN_SUBJECTS is empty; nothing to seed")
		return nil
	}

	usersSvc := services.NewUsersService(s)
	for _, as := range adminSubjects {
		hash := auth.SubjectHash(as.Issuer, as.Subject)
		user, err := usersSvc.GetOrCreateBySubjectHash(ctx, hash, "Admin", true)
		if err != nil {
			return fmt.Errorf("failed to seed admin user for issuer %q: %w", as.Issuer, err)
		}
		fmt.Printf("Admin user ready: issuer=%q subject=%q user_id=%d is_admin=%v\n", as.Issuer, as.Subject, user.ID, user.IsAdmin)
	}
	return nil
}

// cmdAddressesGenerate generates count fresh addresses from the configured
// Dogecoin node and imports them with the given purpose — the same "top up
// the deposit pool" task an operator would otherwise do by hand with
// dogecoin-cli getnewaddress plus `dogecade addresses import`, collapsed
// into one step for scripting (cron, container startup, CI).
func cmdAddressesGenerate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("addresses generate", flag.ExitOnError)
	count := fs.Int("count", 10, "number of addresses to generate and import")
	purpose := fs.String("purpose", "token_deposit", "address purpose: token_deposit or machine_direct")
	_ = fs.Parse(args)

	if *purpose != "token_deposit" && *purpose != "machine_direct" {
		return fmt.Errorf("invalid --purpose %q: must be token_deposit or machine_direct", *purpose)
	}
	if *count < 1 {
		return fmt.Errorf("--count must be at least 1")
	}

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return fmt.Errorf("configuration error: %w", err)
	}
	if cfg.DogecoinRPCURL == "" {
		return fmt.Errorf("DOGECOIND_RPC_URL is not configured; a node is required to generate addresses")
	}

	s, err := store.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer func() { _ = s.Close() }()

	nodeClient, err := corerpc.NewClient(cfg.DogecoinRPCURL, cfg.DogecoinRPCUser, cfg.DogecoinRPCPass)
	if err != nil {
		return fmt.Errorf("failed to create node client: %w", err)
	}

	addrs := make([]string, 0, *count)
	for i := 0; i < *count; i++ {
		addr, err := nodeClient.GetNewAddress(ctx)
		if err != nil {
			return fmt.Errorf("failed to generate address %d/%d: %w", i+1, *count, err)
		}
		addrs = append(addrs, addr)
	}

	batchSvc := services.NewAddressBatchService(s)
	batchID, err := batchSvc.ImportBatch(ctx, "addresses generate", addrs, nodeClient, *purpose)
	if err != nil {
		return fmt.Errorf("failed to import generated addresses: %w", err)
	}
	fmt.Printf("Generated and imported %d addresses (batch %d, purpose %s)\n", len(addrs), batchID, *purpose)
	return nil
}

// cmdMachines dispatches `dogecade machines` subcommands.
func cmdMachines(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: dogecade machines <subcommand> [options]\nSubcommands: create")
	}

	subcommand := args[0]
	subargs := args[1:]

	switch subcommand {
	case "create":
		return cmdMachinesCreate(ctx, subargs)
	default:
		return fmt.Errorf("unknown machines subcommand: %s\nAvailable: create", subcommand)
	}
}

// cmdMachinesCreate is a CLI equivalent of the admin console's "add
// machine" form (POST /admin/machines) — useful for scripting a machine
// into existence without a browser session, e.g. provisioning tooling or
// local setup. Treats an already-taken slug as a success, not an error, so
// it's safe to call repeatedly (container startup, setup scripts).
func cmdMachinesCreate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("machines create", flag.ExitOnError)
	_ = fs.Parse(args)

	if fs.NArg() < 2 {
		return fmt.Errorf("usage: dogecade machines create <slug> <name>")
	}
	slug := fs.Arg(0)
	name := fs.Arg(1)

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return fmt.Errorf("configuration error: %w", err)
	}

	s, err := store.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer func() { _ = s.Close() }()

	machinesSvc := services.NewMachinesService(s)
	id, err := machinesSvc.Create(ctx, slug, name)
	switch {
	case err == nil:
		fmt.Printf("Created machine %q (id %d)\n", slug, id)
	case errors.Is(err, services.ErrMachineSlugTaken):
		fmt.Printf("Machine %q already exists; skipping\n", slug)
	default:
		return fmt.Errorf("failed to create machine: %w", err)
	}
	return nil
}

func cmdRelaysTestFire(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("relays test-fire", flag.ExitOnError)
	_ = fs.Parse(args)

	if fs.NArg() < 1 {
		return fmt.Errorf("usage: dogecade relays test-fire <machine-slug>")
	}
	slug := fs.Arg(0)

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return fmt.Errorf("configuration error: %w", err)
	}

	s, err := store.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer func() { _ = s.Close() }()

	var machineID int64
	err = s.DB().QueryRowContext(ctx, "SELECT id FROM machines WHERE slug = ?", slug).Scan(&machineID)
	if err != nil {
		return fmt.Errorf("failed to find machine %q: %w", slug, err)
	}

	settingsSvc := services.NewSettingsService(s)
	ledgerSvc := services.NewLedgerService(s)
	dispatcher := relay.NewDispatcher(s, ledgerSvc, settingsSvc)

	if err := dispatcher.TestFire(ctx, machineID); err != nil {
		return fmt.Errorf("test-fire failed: %w", err)
	}

	fmt.Printf("Test-fire pulse sent to machine %q\n", slug)
	return nil
}

// cmdRelaysCreateBoard is a CLI equivalent of the admin console's "add relay
// board" form (POST /admin/boards) — useful for scripting a board into
// existence without a browser session, e.g. local setup or provisioning
// tooling. Treats an already-taken name as a success, not an error, so it's
// safe to call repeatedly (container startup, setup scripts).
func cmdRelaysCreateBoard(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("relays create-board", flag.ExitOnError)
	_ = fs.Parse(args)

	if fs.NArg() < 2 {
		return fmt.Errorf("usage: dogecade relays create-board <name> <base-url>")
	}
	name := fs.Arg(0)
	baseURL := fs.Arg(1)

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return fmt.Errorf("configuration error: %w", err)
	}

	s, err := store.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer func() { _ = s.Close() }()

	relaysSvc := services.NewRelaysService(s)
	id, err := relaysSvc.CreateBoard(ctx, name, baseURL)
	switch {
	case err == nil:
		fmt.Printf("Created relay board %q (id %d, base_url %s)\n", name, id, baseURL)
	case errors.Is(err, services.ErrBoardNameTaken):
		fmt.Printf("Relay board %q already exists; skipping\n", name)
	default:
		return fmt.Errorf("failed to create relay board: %w", err)
	}
	return nil
}

// cmdRelaysBind is a CLI equivalent of the admin console's "bind relay"
// form (POST /admin/relays/bind), looking the machine and board up by their
// human-readable slug/name rather than requiring numeric ids. Treats an
// already-bound machine as a success, not an error (ErrBindingConflict), so
// it's safe to call repeatedly against an already-seeded volume.
func cmdRelaysBind(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("relays bind", flag.ExitOnError)
	_ = fs.Parse(args)

	if fs.NArg() < 3 {
		return fmt.Errorf("usage: dogecade relays bind <machine-slug> <board-name> <relay-number>")
	}
	machineSlug := fs.Arg(0)
	boardName := fs.Arg(1)
	relayNumber, err := strconv.Atoi(fs.Arg(2))
	if err != nil {
		return fmt.Errorf("invalid relay-number %q: %w", fs.Arg(2), err)
	}

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return fmt.Errorf("configuration error: %w", err)
	}

	s, err := store.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer func() { _ = s.Close() }()

	var machineID int64
	if err := s.DB().QueryRowContext(ctx, "SELECT id FROM machines WHERE slug = ?", machineSlug).Scan(&machineID); err != nil {
		return fmt.Errorf("failed to find machine %q: %w", machineSlug, err)
	}
	var boardID int64
	if err := s.DB().QueryRowContext(ctx, "SELECT id FROM relay_boards WHERE name = ?", boardName).Scan(&boardID); err != nil {
		return fmt.Errorf("failed to find relay board %q: %w", boardName, err)
	}

	relaysSvc := services.NewRelaysService(s)
	id, err := relaysSvc.Bind(ctx, machineID, boardID, relayNumber)
	switch {
	case err == nil:
		fmt.Printf("Bound machine %q to board %q relay %d (binding %d)\n", machineSlug, boardName, relayNumber, id)
	case errors.Is(err, services.ErrBindingConflict):
		fmt.Printf("Machine %q already has an active relay binding; skipping\n", machineSlug)
	default:
		return fmt.Errorf("failed to bind relay: %w", err)
	}
	return nil
}
