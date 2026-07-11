package corerpc

import (
	"os"
	"path/filepath"
	"testing"
)

// withTempPATH temporarily changes the PATH environment variable and restores it after the test.
// This is useful for testing binary discovery without system-wide effects.
func withTempPATH(t *testing.T, tempDir string, fn func()) {
	t.Helper()
	originalPath := os.Getenv("PATH")
	defer os.Setenv("PATH", originalPath)
	os.Setenv("PATH", tempDir)
	fn()
}

// TestFindDogecoindBinary_NotFound verifies that findDogecoindBinary returns an error
// when the binary is not on PATH. This test runs even without the integration build tag.
func TestFindDogecoindBinary_NotFound(t *testing.T) {
	// Create a temp directory to use as a fake PATH
	tmpDir := t.TempDir()

	withTempPATH(t, tmpDir, func() {
		// Try to find dogecoind; should fail
		_, err := findDogecoindBinary()
		if err == nil {
			t.Fatal("expected findDogecoindBinary to fail when binary not on PATH, got nil error")
		}
	})
}

// TestFindDogecoindBinary_Found verifies that findDogecoindBinary can locate an executable.
// This test uses a fake executable in a temp directory to avoid requiring the real dogecoind.
func TestFindDogecoindBinary_Found(t *testing.T) {
	// Create a temp directory
	tmpDir := t.TempDir()

	// Create a fake dogecoind script (executable file)
	fakeDogecoind := filepath.Join(tmpDir, "dogecoind")
	if err := os.WriteFile(fakeDogecoind, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatalf("failed to create fake dogecoind: %v", err)
	}

	withTempPATH(t, tmpDir, func() {
		// Try to find dogecoind; should succeed
		path, err := findDogecoindBinary()
		if err != nil {
			t.Fatalf("expected findDogecoindBinary to succeed, got error: %v", err)
		}

		// Verify it found our fake executable
		if path != fakeDogecoind {
			t.Errorf("expected path %s, got %s", fakeDogecoind, path)
		}
	})
}
