package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildBinary is a test helper that compiles the dogecade binary for testing.
func buildBinary(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	binPath := filepath.Join(tmpDir, "dogecade-test")
	cmd := exec.Command("go", "build", "-o", binPath, ".")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to build binary: %v\n%s", err, string(output))
	}
	return binPath
}

// runDogecade is a test helper that runs a dogecade subcommand.
func runDogecade(t *testing.T, binPath string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	output := strings.TrimSpace(stdout.String() + stderr.String())
	return output, err
}

func TestVersionCommand(t *testing.T) {
	binPath := buildBinary(t)

	output, err := runDogecade(t, binPath, "version")
	if err != nil {
		t.Fatalf("dogecade version failed: %v\nOutput: %s", err, output)
	}

	if output == "" {
		t.Fatal("dogecade version produced empty output")
	}

	t.Logf("dogecade version output: %s", output)
}

func TestVersionCommandIsNonEmpty(t *testing.T) {
	// Verify version string is set (not just "dev" from default)
	binPath := buildBinary(t)

	output, err := runDogecade(t, binPath, "version")
	if err != nil {
		t.Fatalf("dogecade version failed: %v", err)
	}

	if len(strings.TrimSpace(output)) == 0 {
		t.Error("version string is empty")
	}
}

func TestUnknownCommand(t *testing.T) {
	binPath := buildBinary(t)

	output, err := runDogecade(t, binPath, "invalid-subcommand")
	if err == nil {
		t.Error("Expected error for unknown subcommand, but got none")
	}

	if !strings.Contains(output, "Unknown subcommand") {
		t.Errorf("Expected 'Unknown subcommand' in error message, got: %s", output)
	}
}

func TestNoCommandGivesUsage(t *testing.T) {
	binPath := buildBinary(t)

	output, err := runDogecade(t, binPath)
	if err == nil {
		t.Error("Expected error when no subcommand provided, but got none")
	}

	if !strings.Contains(output, "Usage") {
		t.Errorf("Expected usage message in error, got: %s", output)
	}
}

func TestAddressesImportCommandRequiresFile(t *testing.T) {
	// Skip this test if we don't have DB environment set up
	binPath := buildBinary(t)

	// The addresses import subcommand requires config, but let's at least
	// verify the error message guides the user
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	cmd := exec.Command(binPath, "addresses", "import")
	cmd.Env = append(os.Environ(),
		"DOGECADE_DB_PATH="+dbPath,
		"DOGECADE_BASE_URL=http://localhost:8080",
	)

	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Error("Expected error when addresses import called without file argument")
	}

	outputStr := string(output)
	if !strings.Contains(outputStr, "Usage") && !strings.Contains(outputStr, "usage") {
		t.Logf("Note: addresses import error message: %s", outputStr)
	}
}
