package corerpc

import (
	"os/exec"
)

// findDogecoindBinary locates the dogecoind binary on PATH.
// Returns the path to the binary or an error if not found.
// This function is available in all builds and can be tested without integration tags.
func findDogecoindBinary() (string, error) {
	return exec.LookPath("dogecoind")
}
