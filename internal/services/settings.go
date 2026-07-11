package services

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"

	"github.com/chromatic/dogecade/internal/config"
	"github.com/chromatic/dogecade/internal/store"
)

const (
	// Known settings keys with their defaults.
	keyMinConfirmations    = "min_confirmations"
	keyZeroConfMaxKoinu    = "zero_conf_max_koinu"
	keyPoolWarnThreshold   = "pool_warn_threshold"
	keyPoolUrgentThreshold = "pool_urgent_threshold"
	keyChainCursor         = "chain_cursor"
	keyTokenPriceKoinu     = "token_price_koinu"
	keyRelayPulseGapMs     = "relay_pulse_gap_ms"
	keyRelayMaxAttempts    = "relay_max_attempts"
	keyNodeRPCURL          = "node_rpc_url"
	keyNodeRPCUser         = "node_rpc_user"
	keyNodeRPCPass         = "node_rpc_pass"
	keyNodeZMQAddr         = "node_zmq_addr"

	keyDirectPayMaxCreditsPerTx     = "direct_pay_max_credits_per_tx"
	keyDirectPayRotateIntervalHours = "direct_pay_rotate_interval_hours"
	keyDirectPayRotateAfterUses     = "direct_pay_rotate_after_uses"

	defaultMinConfirmations    = 1
	defaultZeroConfMaxKoinu    = 0
	defaultPoolWarnThreshold   = 25
	defaultPoolUrgentThreshold = 10
	defaultChainCursor         = ""          // empty = start from genesis
	defaultTokenPriceKoinu     = 100_000_000 // 1 DOGE per token
	defaultRelayPulseGapMs     = 750         // spacing between pulses to the same machine
	defaultRelayMaxAttempts    = 5           // dispatch attempts before a pulse is marked failed

	defaultDirectPayMaxCreditsPerTx     = 10 // caps credits from one oversized direct payment
	defaultDirectPayRotateIntervalHours = 0  // 0 = interval-based rotation disabled
	defaultDirectPayRotateAfterUses     = 0  // 0 = use-count-based rotation disabled
)

// SettingsService provides typed access to application settings stored in
// the settings table, with sensible defaults for unset values.
type SettingsService struct {
	store *store.Store
}

// NewSettingsService creates a new SettingsService wrapping the given Store.
func NewSettingsService(s *store.Store) *SettingsService {
	return &SettingsService{store: s}
}

// GetMinConfirmations returns the minimum number of confirmations required
// for a deposit to be credited. If not set, returns the default of 1.
func (svc *SettingsService) GetMinConfirmations(ctx context.Context) (int, error) {
	value, err := svc.getString(ctx, keyMinConfirmations)
	if err != nil {
		return 0, err
	}
	if value == "" {
		return defaultMinConfirmations, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid min_confirmations value %q: %w", value, err)
	}
	return int(parsed), nil
}

// SetMinConfirmations sets the minimum number of confirmations required for
// a deposit to be credited. Creates or updates the setting in the database.
func (svc *SettingsService) SetMinConfirmations(ctx context.Context, value int) error {
	return svc.setString(ctx, keyMinConfirmations, strconv.FormatInt(int64(value), 10))
}

// GetZeroConfMaxKoinu returns the maximum amount (in koinu) that can be
// credited on mempool acceptance (0-conf). If not set, returns the default
// of 0 (disabled).
func (svc *SettingsService) GetZeroConfMaxKoinu(ctx context.Context) (int64, error) {
	value, err := svc.getString(ctx, keyZeroConfMaxKoinu)
	if err != nil {
		return 0, err
	}
	if value == "" {
		return defaultZeroConfMaxKoinu, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid zero_conf_max_koinu value %q: %w", value, err)
	}
	return parsed, nil
}

// SetZeroConfMaxKoinu sets the maximum amount (in koinu) that can be
// credited on mempool acceptance (0-conf). Creates or updates the setting
// in the database.
func (svc *SettingsService) SetZeroConfMaxKoinu(ctx context.Context, value int64) error {
	return svc.setString(ctx, keyZeroConfMaxKoinu, strconv.FormatInt(value, 10))
}

// GetPoolWarnThreshold returns the pool count below which a warning alert is fired.
// If not set, returns the default of 25.
func (svc *SettingsService) GetPoolWarnThreshold(ctx context.Context) (int, error) {
	value, err := svc.getString(ctx, keyPoolWarnThreshold)
	if err != nil {
		return 0, err
	}
	if value == "" {
		return defaultPoolWarnThreshold, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid pool_warn_threshold value %q: %w", value, err)
	}
	return int(parsed), nil
}

// SetPoolWarnThreshold sets the pool count below which a warning alert is fired.
// Creates or updates the setting in the database.
func (svc *SettingsService) SetPoolWarnThreshold(ctx context.Context, value int) error {
	return svc.setString(ctx, keyPoolWarnThreshold, strconv.FormatInt(int64(value), 10))
}

// GetPoolUrgentThreshold returns the pool count below which an urgent alert is fired.
// If not set, returns the default of 10.
func (svc *SettingsService) GetPoolUrgentThreshold(ctx context.Context) (int, error) {
	value, err := svc.getString(ctx, keyPoolUrgentThreshold)
	if err != nil {
		return 0, err
	}
	if value == "" {
		return defaultPoolUrgentThreshold, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid pool_urgent_threshold value %q: %w", value, err)
	}
	return int(parsed), nil
}

// SetPoolUrgentThreshold sets the pool count below which an urgent alert is fired.
// Creates or updates the setting in the database.
func (svc *SettingsService) SetPoolUrgentThreshold(ctx context.Context, value int) error {
	return svc.setString(ctx, keyPoolUrgentThreshold, strconv.FormatInt(int64(value), 10))
}

// GetChainCursor returns the persisted block hash cursor for chain scanning.
// Empty string means start from genesis. Used by ChainWatcher to track progress.
func (svc *SettingsService) GetChainCursor(ctx context.Context) (string, error) {
	value, err := svc.getString(ctx, keyChainCursor)
	if err != nil {
		return "", err
	}
	if value == "" {
		return defaultChainCursor, nil
	}
	return value, nil
}

// SetChainCursor persists the block hash cursor for chain scanning.
// Called by ChainWatcher after processing a batch of transactions.
func (svc *SettingsService) SetChainCursor(ctx context.Context, blockHash string) error {
	return svc.setString(ctx, keyChainCursor, blockHash)
}

// GetTokenPriceKoinu returns the price of one token in koinu, used to convert
// a deposit's amount_koinu into whole tokens (floor division). If not set,
// returns the default of 100,000,000 koinu (1 DOGE per token).
func (svc *SettingsService) GetTokenPriceKoinu(ctx context.Context) (int64, error) {
	value, err := svc.getString(ctx, keyTokenPriceKoinu)
	if err != nil {
		return 0, err
	}
	if value == "" {
		return defaultTokenPriceKoinu, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid token_price_koinu value %q: %w", value, err)
	}
	return parsed, nil
}

// SetTokenPriceKoinu sets the price of one token in koinu. Creates or updates
// the setting in the database.
func (svc *SettingsService) SetTokenPriceKoinu(ctx context.Context, value int64) error {
	return svc.setString(ctx, keyTokenPriceKoinu, strconv.FormatInt(value, 10))
}

// GetRelayPulseGapMs returns the minimum spacing, in milliseconds, between
// pulses sent to the same machine, so its coin-switch scan matrix can
// register each pulse separately. If not set, returns the default of 750ms.
func (svc *SettingsService) GetRelayPulseGapMs(ctx context.Context) (int, error) {
	value, err := svc.getString(ctx, keyRelayPulseGapMs)
	if err != nil {
		return 0, err
	}
	if value == "" {
		return defaultRelayPulseGapMs, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid relay_pulse_gap_ms value %q: %w", value, err)
	}
	return int(parsed), nil
}

// SetRelayPulseGapMs sets the minimum spacing, in milliseconds, between
// pulses sent to the same machine.
func (svc *SettingsService) SetRelayPulseGapMs(ctx context.Context, value int) error {
	return svc.setString(ctx, keyRelayPulseGapMs, strconv.FormatInt(int64(value), 10))
}

// GetRelayMaxAttempts returns the number of dispatch attempts allowed for a
// credit pulse before it's marked failed and refunded. If not set, returns
// the default of 5.
func (svc *SettingsService) GetRelayMaxAttempts(ctx context.Context) (int, error) {
	value, err := svc.getString(ctx, keyRelayMaxAttempts)
	if err != nil {
		return 0, err
	}
	if value == "" {
		return defaultRelayMaxAttempts, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid relay_max_attempts value %q: %w", value, err)
	}
	return int(parsed), nil
}

// SetRelayMaxAttempts sets the number of dispatch attempts allowed for a
// credit pulse before it's marked failed and refunded.
func (svc *SettingsService) SetRelayMaxAttempts(ctx context.Context, value int) error {
	return svc.setString(ctx, keyRelayMaxAttempts, strconv.FormatInt(int64(value), 10))
}

// GetDirectPayMaxCreditsPerTx returns the maximum number of credit pulses a
// single direct-pay deposit can generate, guarding against one oversized
// payment queuing an unbounded number of physical pulses. If not set,
// returns the default of 10.
func (svc *SettingsService) GetDirectPayMaxCreditsPerTx(ctx context.Context) (int, error) {
	value, err := svc.getString(ctx, keyDirectPayMaxCreditsPerTx)
	if err != nil {
		return 0, err
	}
	if value == "" {
		return defaultDirectPayMaxCreditsPerTx, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid direct_pay_max_credits_per_tx value %q: %w", value, err)
	}
	return int(parsed), nil
}

// SetDirectPayMaxCreditsPerTx sets the maximum number of credit pulses a
// single direct-pay deposit can generate.
func (svc *SettingsService) SetDirectPayMaxCreditsPerTx(ctx context.Context, value int) error {
	return svc.setString(ctx, keyDirectPayMaxCreditsPerTx, strconv.FormatInt(int64(value), 10))
}

// GetDirectPayRotateIntervalHours returns the number of hours a direct-pay
// address stays active before the rotation job replaces it. 0 (the default)
// disables interval-based rotation.
func (svc *SettingsService) GetDirectPayRotateIntervalHours(ctx context.Context) (int, error) {
	value, err := svc.getString(ctx, keyDirectPayRotateIntervalHours)
	if err != nil {
		return 0, err
	}
	if value == "" {
		return defaultDirectPayRotateIntervalHours, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid direct_pay_rotate_interval_hours value %q: %w", value, err)
	}
	return int(parsed), nil
}

// SetDirectPayRotateIntervalHours sets the interval-based rotation period.
func (svc *SettingsService) SetDirectPayRotateIntervalHours(ctx context.Context, value int) error {
	return svc.setString(ctx, keyDirectPayRotateIntervalHours, strconv.FormatInt(int64(value), 10))
}

// GetDirectPayRotateAfterUses returns the number of payments a direct-pay
// address accepts before the rotation job replaces it. 0 (the default)
// disables use-count-based rotation.
func (svc *SettingsService) GetDirectPayRotateAfterUses(ctx context.Context) (int, error) {
	value, err := svc.getString(ctx, keyDirectPayRotateAfterUses)
	if err != nil {
		return 0, err
	}
	if value == "" {
		return defaultDirectPayRotateAfterUses, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid direct_pay_rotate_after_uses value %q: %w", value, err)
	}
	return int(parsed), nil
}

// SetDirectPayRotateAfterUses sets the use-count-based rotation threshold.
func (svc *SettingsService) SetDirectPayRotateAfterUses(ctx context.Context, value int) error {
	return svc.setString(ctx, keyDirectPayRotateAfterUses, strconv.FormatInt(int64(value), 10))
}

// NodeRPCConfig holds the Dogecoin node connection settings editable from
// the admin console (7.3). It mirrors config.Config's node fields but is
// sourced from the settings table (seeded once from the environment at
// first boot) rather than read fresh from the environment on every access.
type NodeRPCConfig struct {
	RPCURL  string
	RPCUser string
	RPCPass string
	ZMQAddr string
}

// GetNodeRPCConfig returns the currently saved node RPC/ZMQ connection
// settings. Empty fields mean "not configured yet".
func (svc *SettingsService) GetNodeRPCConfig(ctx context.Context) (NodeRPCConfig, error) {
	var cfg NodeRPCConfig
	var err error
	if cfg.RPCURL, err = svc.getString(ctx, keyNodeRPCURL); err != nil {
		return NodeRPCConfig{}, err
	}
	if cfg.RPCUser, err = svc.getString(ctx, keyNodeRPCUser); err != nil {
		return NodeRPCConfig{}, err
	}
	if cfg.RPCPass, err = svc.getString(ctx, keyNodeRPCPass); err != nil {
		return NodeRPCConfig{}, err
	}
	if cfg.ZMQAddr, err = svc.getString(ctx, keyNodeZMQAddr); err != nil {
		return NodeRPCConfig{}, err
	}
	return cfg, nil
}

// SetNodeRPCConfig saves the node RPC/ZMQ connection settings edited via the
// admin console. Note: the live node client and chain watcher are
// constructed once at boot from these settings (seeded from the
// environment); saving new values here takes effect after a service
// restart, not immediately — this is documented on the admin settings page.
func (svc *SettingsService) SetNodeRPCConfig(ctx context.Context, cfg NodeRPCConfig) error {
	if err := svc.setString(ctx, keyNodeRPCURL, cfg.RPCURL); err != nil {
		return err
	}
	if err := svc.setString(ctx, keyNodeRPCUser, cfg.RPCUser); err != nil {
		return err
	}
	if err := svc.setString(ctx, keyNodeRPCPass, cfg.RPCPass); err != nil {
		return err
	}
	return svc.setString(ctx, keyNodeZMQAddr, cfg.ZMQAddr)
}

// SeedFromEnv initializes settings from environment config on first boot.
// If a setting already exists in the database, it is not overwritten (so a
// later admin edit via the settings page is never clobbered by re-seeding
// on a subsequent restart). min_confirmations and zero_conf_max_koinu
// remain admin-set only (not sourced from environment).
func (svc *SettingsService) SeedFromEnv(ctx context.Context, cfg config.Config) error {
	if err := svc.setStringIfAbsent(ctx, keyNodeRPCURL, cfg.DogecoinRPCURL); err != nil {
		return err
	}
	if err := svc.setStringIfAbsent(ctx, keyNodeRPCUser, cfg.DogecoinRPCUser); err != nil {
		return err
	}
	if err := svc.setStringIfAbsent(ctx, keyNodeRPCPass, cfg.DogecoinRPCPass); err != nil {
		return err
	}
	if err := svc.setStringIfAbsent(ctx, keyNodeZMQAddr, cfg.DogecoinZMQAddr); err != nil {
		return err
	}
	return nil
}

// setStringIfAbsent sets key to value only if it doesn't already exist,
// used for environment seeding so an existing (possibly admin-edited)
// setting is never overwritten by a restart.
func (svc *SettingsService) setStringIfAbsent(ctx context.Context, key, value string) error {
	if value == "" {
		return nil
	}
	existing, err := svc.getString(ctx, key)
	if err != nil {
		return err
	}
	if existing != "" {
		return nil
	}
	return svc.setString(ctx, key, value)
}

// getString retrieves a setting value by key from the database.
// Returns an empty string if the key does not exist.
func (svc *SettingsService) getString(ctx context.Context, key string) (string, error) {
	var value string
	err := svc.store.DB().QueryRowContext(
		ctx,
		"SELECT value FROM settings WHERE key = ?",
		key,
	).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to query setting %q: %w", key, err)
	}
	return value, nil
}

// setString inserts or updates a setting value by key in the database.
func (svc *SettingsService) setString(ctx context.Context, key, value string) error {
	_, err := svc.store.DB().ExecContext(
		ctx,
		"INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = ?",
		key, value, value,
	)
	if err != nil {
		return fmt.Errorf("failed to set setting %q: %w", key, err)
	}
	return nil
}
