// Package config loads and validates environment-based configuration for dogecade.
//
// Configuration is loaded from environment variables and validated at startup.
// Required environment variables are DOGECADE_DB_PATH and DOGECADE_BASE_URL.
// Optional environment variables include DOGECADE_LISTEN_ADDR (default ":8080"),
// DOGECOIND_RPC_URL/USER/PASS, DOGECOIND_ZMQ_ADDR, and DOGECADE_ADMIN_SUBJECTS.
//
// The Load function takes a getenv function to allow for dependency injection
// and testability without touching the actual environment.
package config
