//go:build !windows

package bridge

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
)

// binaryDirs are searched when a CLI is not on PATH.
func binaryDirs(home string) []string {
	return []string{filepath.Join(home, ".local/bin"), "/opt/homebrew/bin", "/usr/local/bin",
		filepath.Join(home, ".npm-global/bin"), filepath.Join(home, ".bun/bin")}
}

var binaryExts = []string{""}

func isExecutable(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir() && st.Mode()&0o111 != 0
}

func command(ctx context.Context, bin string, args ...string) (*exec.Cmd, error) {
	return exec.CommandContext(ctx, bin, args...), nil
}
