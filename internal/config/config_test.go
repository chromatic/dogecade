package config

import (
	"strings"
	"testing"
)

// fakeGetenv returns an environment getter that uses a map of values.
// Useful for testing without touching the real environment.
func fakeGetenv(envMap map[string]string) func(string) string {
	return func(key string) string {
		return envMap[key]
	}
}

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    Config
		wantErr string // substring of error message, or "" for no error
	}{
		{
			name: "all required vars set",
			env: map[string]string{
				"DOGECADE_DB_PATH":  "/data/dogecade.db",
				"DOGECADE_BASE_URL": "http://localhost:8080",
			},
			want: Config{
				DBPath:          "/data/dogecade.db",
				BaseURL:         "http://localhost:8080",
				ListenAddr:      ":8080", // default
				AdminSubjects:   "",
				DogecoinRPCURL:  "",
				DogecoinRPCUser: "",
				DogecoinRPCPass: "",
				DogecoinZMQAddr: "",
			},
		},
		{
			name: "all optional vars set",
			env: map[string]string{
				"DOGECADE_DB_PATH":        "/data/dogecade.db",
				"DOGECADE_BASE_URL":       "https://arcade.example.com",
				"DOGECADE_LISTEN_ADDR":    ":9000",
				"DOGECADE_ADMIN_SUBJECTS": "https://accounts.google.com|user@example.com,https://keycloak.local|admin",
				"DOGECOIND_RPC_URL":       "http://localhost:18332",
				"DOGECOIND_RPC_USER":      "dogeuser",
				"DOGECOIND_RPC_PASS":      "dogepass",
				"DOGECOIND_ZMQ_ADDR":      "tcp://localhost:28332",
			},
			want: Config{
				DBPath:          "/data/dogecade.db",
				BaseURL:         "https://arcade.example.com",
				ListenAddr:      ":9000",
				AdminSubjects:   "https://accounts.google.com|user@example.com,https://keycloak.local|admin",
				DogecoinRPCURL:  "http://localhost:18332",
				DogecoinRPCUser: "dogeuser",
				DogecoinRPCPass: "dogepass",
				DogecoinZMQAddr: "tcp://localhost:28332",
			},
		},
		{
			name: "missing DOGECADE_DB_PATH",
			env: map[string]string{
				"DOGECADE_BASE_URL": "http://localhost:8080",
			},
			wantErr: "DOGECADE_DB_PATH",
		},
		{
			name: "missing DOGECADE_BASE_URL",
			env: map[string]string{
				"DOGECADE_DB_PATH": "/data/dogecade.db",
			},
			wantErr: "DOGECADE_BASE_URL",
		},
		{
			name: "invalid BaseURL format",
			env: map[string]string{
				"DOGECADE_DB_PATH":  "/data/dogecade.db",
				"DOGECADE_BASE_URL": "not a valid url",
			},
			wantErr: "DOGECADE_BASE_URL",
		},
		{
			name: "BaseURL with scheme missing",
			env: map[string]string{
				"DOGECADE_DB_PATH":  "/data/dogecade.db",
				"DOGECADE_BASE_URL": "://invalid",
			},
			wantErr: "DOGECADE_BASE_URL",
		},
		{
			name: "invalid ListenAddr format (no colon)",
			env: map[string]string{
				"DOGECADE_DB_PATH":     "/data/dogecade.db",
				"DOGECADE_BASE_URL":    "http://localhost:8080",
				"DOGECADE_LISTEN_ADDR": "8080",
			},
			wantErr: "DOGECADE_LISTEN_ADDR",
		},
		{
			name: "invalid ListenAddr format (port not numeric)",
			env: map[string]string{
				"DOGECADE_DB_PATH":     "/data/dogecade.db",
				"DOGECADE_BASE_URL":    "http://localhost:8080",
				"DOGECADE_LISTEN_ADDR": ":abc",
			},
			wantErr: "DOGECADE_LISTEN_ADDR",
		},
		{
			name: "valid ListenAddr with hostname",
			env: map[string]string{
				"DOGECADE_DB_PATH":     "/data/dogecade.db",
				"DOGECADE_BASE_URL":    "http://localhost:8080",
				"DOGECADE_LISTEN_ADDR": "127.0.0.1:8080",
			},
			want: Config{
				DBPath:          "/data/dogecade.db",
				BaseURL:         "http://localhost:8080",
				ListenAddr:      "127.0.0.1:8080",
				AdminSubjects:   "",
				DogecoinRPCURL:  "",
				DogecoinRPCUser: "",
				DogecoinRPCPass: "",
				DogecoinZMQAddr: "",
			},
		},
		{
			name: "ListenAddr with IPv6",
			env: map[string]string{
				"DOGECADE_DB_PATH":     "/data/dogecade.db",
				"DOGECADE_BASE_URL":    "http://localhost:8080",
				"DOGECADE_LISTEN_ADDR": "[::1]:8080",
			},
			want: Config{
				DBPath:          "/data/dogecade.db",
				BaseURL:         "http://localhost:8080",
				ListenAddr:      "[::1]:8080",
				AdminSubjects:   "",
				DogecoinRPCURL:  "",
				DogecoinRPCUser: "",
				DogecoinRPCPass: "",
				DogecoinZMQAddr: "",
			},
		},
		{
			name: "only some optional vars set",
			env: map[string]string{
				"DOGECADE_DB_PATH":  "/data/dogecade.db",
				"DOGECADE_BASE_URL": "http://localhost:8080",
				"DOGECOIND_RPC_URL": "http://localhost:18332",
			},
			want: Config{
				DBPath:          "/data/dogecade.db",
				BaseURL:         "http://localhost:8080",
				ListenAddr:      ":8080", // default
				AdminSubjects:   "",
				DogecoinRPCURL:  "http://localhost:18332",
				DogecoinRPCUser: "",
				DogecoinRPCPass: "",
				DogecoinZMQAddr: "",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Load(fakeGetenv(tt.env))

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("Load() error = nil, wantErr containing %q", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Load() error = %v, wantErr containing %q", err, tt.wantErr)
				}
			} else {
				if err != nil {
					t.Fatalf("Load() unexpected error: %v", err)
				}
				if got != tt.want {
					t.Errorf("Load() got %+v, want %+v", got, tt.want)
				}
			}
		})
	}
}

// TestListenAddrValidation tests address format validation more thoroughly
func TestListenAddrValidation(t *testing.T) {
	tests := []struct {
		name    string
		addr    string
		wantErr bool
	}{
		{"valid default", ":8080", false},
		{"valid with localhost", "localhost:8080", false},
		{"valid with 127.0.0.1", "127.0.0.1:8080", false},
		{"valid with 0.0.0.0", "0.0.0.0:8080", false},
		{"valid IPv6", "[::1]:8080", false},
		{"valid IPv6 with full addr", "[2001:db8::1]:8080", false},
		{"missing colon", "8080", true},
		{"non-numeric port", ":abc", true},
		{"port out of range", "localhost:99999", true},
		{"missing port", "localhost:", true},
		{"port zero", ":0", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateListenAddr(tt.addr)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateListenAddr(%q) error = %v, wantErr %v", tt.addr, err, tt.wantErr)
			}
		})
	}
}

// TestBaseURLValidation tests URL format validation
func TestBaseURLValidation(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"valid http", "http://localhost:8080", false},
		{"valid https", "https://arcade.example.com", false},
		{"valid with IP", "http://127.0.0.1:8080", false},
		{"valid with trailing slash", "https://arcade.example.com/", false},
		{"valid IPv6", "http://[::1]:8080", false},
		{"invalid - not a URL", "not a valid url", true},
		{"invalid - missing scheme", "://missing-scheme", true},
		{"invalid - no scheme", "localhost:8080", true},
		{"invalid - empty", "", true},
		{"invalid - ftp scheme", "ftp://example.com", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateBaseURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateBaseURL(%q) error = %v, wantErr %v", tt.url, err, tt.wantErr)
			}
		})
	}
}
