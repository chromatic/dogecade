package config

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
)

// Config holds all environment-based configuration for dogecade.
// All money values are in koinu (int64). Fields are populated from
// environment variables; required fields must be set, others have defaults.
type Config struct {
	// Required configuration
	DBPath  string // Path to SQLite database file (DOGECADE_DB_PATH)
	BaseURL string // Base URL for the service (DOGECADE_BASE_URL)

	// Optional configuration with defaults
	ListenAddr string // Address to listen on (DOGECADE_LISTEN_ADDR, default ":8080")

	// Optional Dogecoin node configuration (env vars, stored but consumed later)
	DogecoinRPCURL  string // DOGECOIND_RPC_URL
	DogecoinRPCUser string // DOGECOIND_RPC_USER
	DogecoinRPCPass string // DOGECOIND_RPC_PASS
	DogecoinZMQAddr string // DOGECOIND_ZMQ_ADDR

	// Optional admin configuration (env vars, stored but consumed later)
	AdminSubjects string // Comma-separated issuer|subject pairs (DOGECADE_ADMIN_SUBJECTS)

	// Optional OIDC configuration. All three must be set together for OIDC
	// login to be enabled; unset is a first-class "not configured" state
	// (the server still runs, customer sign-in is just unavailable), same
	// pattern as the Dogecoin node RPC config above.
	OIDCIssuerURL    string // DOGECADE_OIDC_ISSUER_URL
	OIDCClientID     string // DOGECADE_OIDC_CLIENT_ID
	OIDCClientSecret string // DOGECADE_OIDC_CLIENT_SECRET

	// SessionSecret signs session cookies (HMAC). If unset, a random secret
	// is generated at boot (logged as a warning): the server still runs,
	// but every restart invalidates existing sessions.
	SessionSecret string // DOGECADE_SESSION_SECRET
}

// OIDCConfigured reports whether all required OIDC settings are present.
func (c Config) OIDCConfigured() bool {
	return c.OIDCIssuerURL != "" && c.OIDCClientID != "" && c.OIDCClientSecret != ""
}

// Load reads environment variables via the provided getenv function and
// returns a validated Config. The getenv function allows for testability
// without touching the actual environment.
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{}

	// Required fields
	cfg.DBPath = getenv("DOGECADE_DB_PATH")
	if cfg.DBPath == "" {
		return cfg, fmt.Errorf("DOGECADE_DB_PATH is required")
	}

	cfg.BaseURL = getenv("DOGECADE_BASE_URL")
	if cfg.BaseURL == "" {
		return cfg, fmt.Errorf("DOGECADE_BASE_URL is required")
	}
	if err := validateBaseURL(cfg.BaseURL); err != nil {
		return cfg, err
	}

	// Optional fields with defaults
	cfg.ListenAddr = getenv("DOGECADE_LISTEN_ADDR")
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":8080"
	}
	if err := validateListenAddr(cfg.ListenAddr); err != nil {
		return cfg, err
	}

	// Optional Dogecoin node config (parsed but not consumed until phase 1.4+)
	cfg.DogecoinRPCURL = getenv("DOGECOIND_RPC_URL")
	cfg.DogecoinRPCUser = getenv("DOGECOIND_RPC_USER")
	cfg.DogecoinRPCPass = getenv("DOGECOIND_RPC_PASS")
	cfg.DogecoinZMQAddr = getenv("DOGECOIND_ZMQ_ADDR")

	// Optional admin config
	cfg.AdminSubjects = getenv("DOGECADE_ADMIN_SUBJECTS")

	// Optional OIDC config: all-or-nothing.
	cfg.OIDCIssuerURL = getenv("DOGECADE_OIDC_ISSUER_URL")
	cfg.OIDCClientID = getenv("DOGECADE_OIDC_CLIENT_ID")
	cfg.OIDCClientSecret = getenv("DOGECADE_OIDC_CLIENT_SECRET")
	oidcFieldsSet := 0
	for _, v := range []string{cfg.OIDCIssuerURL, cfg.OIDCClientID, cfg.OIDCClientSecret} {
		if v != "" {
			oidcFieldsSet++
		}
	}
	if oidcFieldsSet != 0 && oidcFieldsSet != 3 {
		return cfg, fmt.Errorf("DOGECADE_OIDC_ISSUER_URL, DOGECADE_OIDC_CLIENT_ID, and DOGECADE_OIDC_CLIENT_SECRET must all be set together, or all left unset")
	}

	cfg.SessionSecret = getenv("DOGECADE_SESSION_SECRET")

	return cfg, nil
}

// validateBaseURL checks that the base URL is a valid HTTP/HTTPS URL.
func validateBaseURL(baseURL string) error {
	u, err := url.Parse(baseURL)
	if err != nil {
		return fmt.Errorf("DOGECADE_BASE_URL parse error: %w", err)
	}

	if u.Scheme == "" {
		return fmt.Errorf("DOGECADE_BASE_URL must have a scheme (http or https)")
	}

	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("DOGECADE_BASE_URL scheme must be http or https, got %q", u.Scheme)
	}

	if u.Host == "" {
		return fmt.Errorf("DOGECADE_BASE_URL must have a host")
	}

	return nil
}

// validateListenAddr checks that the listen address is in host:port format
// with a valid numeric port.
func validateListenAddr(addr string) error {
	if addr == "" {
		return fmt.Errorf("DOGECADE_LISTEN_ADDR is empty")
	}

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("DOGECADE_LISTEN_ADDR must be in host:port format: %w", err)
	}

	if port == "" {
		return fmt.Errorf("DOGECADE_LISTEN_ADDR must include a port")
	}

	// Validate port is numeric and in range
	portNum, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return fmt.Errorf("DOGECADE_LISTEN_ADDR port must be numeric: %w", err)
	}

	if portNum == 0 {
		return fmt.Errorf("DOGECADE_LISTEN_ADDR port must be > 0")
	}

	// If host is specified, validate it's a valid hostname or IP
	if host != "" {
		// Try to parse as IP first
		ip := net.ParseIP(host)
		if ip == nil {
			// Not an IP, check if it's a valid hostname
			// net.SplitHostPort already validated basic format, so allow it
			if !isValidHostname(host) {
				return fmt.Errorf("DOGECADE_LISTEN_ADDR host %q is not a valid hostname or IP", host)
			}
		}
	}

	return nil
}

// isValidHostname checks if a string is a valid hostname.
// It's a simple check: alphanumeric, dots, and hyphens allowed.
func isValidHostname(h string) bool {
	if h == "" {
		return false
	}
	// Allow localhost and numeric IPs
	if h == "localhost" {
		return true
	}
	for i, c := range h {
		switch {
		case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
		case c >= '0' && c <= '9':
		case c == '.' || c == '-':
			// Hyphens and dots are ok, but not at start/end
			if i == 0 || i == len(h)-1 {
				return false
			}
		default:
			return false
		}
	}
	return true
}
