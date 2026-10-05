//go:build !windows

package bridge

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

// binaryDirs are searched when a CLI is not on PATH: package managers and Node version managers.
func binaryDirs(home string) []string {
	dirs := []string{filepath.Join(home, ".local/bin"), "/opt/homebrew/bin", "/usr/local/bin", "/opt/local/bin",
		filepath.Join(home, ".npm-global/bin"), filepath.Join(home, ".bun/bin"), filepath.Join(home, ".volta/bin"),
		filepath.Join(home, "Library/pnpm"), filepath.Join(home, ".local/share/pnpm"),
		filepath.Join(home, ".asdf/shims"), filepath.Join(home, ".local/share/mise/shims"),
		filepath.Join(home, ".local/share/fnm/aliases/default/bin"),
		filepath.Join(home, "Library/Application Support/fnm/aliases/default/bin")}
	nvm := os.Getenv("NVM_DIR")
	if nvm == "" {
		nvm = filepath.Join(home, ".nvm")
	}
	return append(dirs, nodeVersionDirs(filepath.Join(nvm, "versions/node"))...)
}

// nodeVersionDirs lists <root>/v*/bin, newest Node version first.
func nodeVersionDirs(root string) []string {
	entries, _ := os.ReadDir(root)
	var versions []string
	for _, e := range entries {
		if e.IsDir() {
			versions = append(versions, e.Name())
		}
	}
	slices.SortFunc(versions, func(a, b string) int { return compareVersions(b, a) })
	dirs := make([]string, len(versions))
	for i, v := range versions {
		dirs[i] = filepath.Join(root, v, "bin")
	}
	return dirs
}

// compareVersions orders "v22.1.0"-style names numerically.
func compareVersions(a, b string) int {
	pa, pb := strings.Split(strings.TrimPrefix(a, "v"), "."), strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < max(len(pa), len(pb)); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x - y
		}
	}
	return 0
}

const pathMarker = "__AI_BRIDGE_ENV__"

// loginShellPath returns PATH as the user's interactive login shell sets it (what `which` sees in a terminal).
func loginShellPath() []string {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
		if runtime.GOOS == "darwin" {
			shell = "/bin/zsh"
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// env prints PATH colon-separated in every shell (fish quotes $PATH as a space-separated list).
	cmd := exec.CommandContext(ctx, shell, "-ilc", "echo "+pathMarker+"; /usr/bin/env")
	cmd.WaitDelay = time.Second
	out, _ := cmd.Output()
	return parseShellPath(string(out))
}

// parseShellPath finds PATH in `env` output printed after the marker (rc files may print banners first).
func parseShellPath(out string) []string {
	_, env, ok := strings.Cut(out, pathMarker+"\n")
	if !ok {
		return nil
	}
	for _, line := range strings.Split(env, "\n") {
		if v, ok := strings.CutPrefix(line, "PATH="); ok {
			return filepath.SplitList(strings.TrimSpace(v))
		}
	}
	return nil
}

func command(ctx context.Context, bin string, args ...string) (*exec.Cmd, error) {
	return exec.CommandContext(ctx, bin, args...), nil
}
