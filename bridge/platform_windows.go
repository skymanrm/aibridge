package bridge

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

const createNoWindow = 0x08000000

// npmShim matches the script an npm .cmd shim hands to node: "%dp0%\node_modules\…\cli.js" %*
var npmShim = regexp.MustCompile(`"%~?dp0%?\\([^"]+)"\s*%\*`)

// binaryDirs are searched when a CLI is not on PATH.
func binaryDirs(home string) []string {
	dirs := []string{filepath.Join(home, ".local", "bin"), filepath.Join(home, ".bun", "bin"),
		filepath.Join(home, "scoop", "shims")}
	if d := os.Getenv("APPDATA"); d != "" {
		dirs = append(dirs, filepath.Join(d, "npm"))
	}
	if d := os.Getenv("LOCALAPPDATA"); d != "" {
		dirs = append(dirs, filepath.Join(d, "Volta", "bin"), filepath.Join(d, "pnpm"))
	}
	if d := os.Getenv("ProgramFiles"); d != "" {
		dirs = append(dirs, filepath.Join(d, "nodejs"), filepath.Join(d, "Docker", "Docker", "resources", "bin"))
	}
	return dirs
}

// loginShellPath is a no-op: Windows apps get the user's full PATH from the registry.
func loginShellPath() []string { return nil }

func isExecutable(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// command starts CLIs without a console window and kills the whole process tree on cancel.
// Batch files never run through cmd.exe: its quoting would let prompt text inject commands.
func command(ctx context.Context, bin string, args ...string) (*exec.Cmd, error) {
	if ext := strings.ToLower(filepath.Ext(bin)); ext == ".cmd" || ext == ".bat" {
		node, script, err := resolveNpmShim(bin)
		if err != nil {
			return nil, err
		}
		bin, args = node, append([]string{script}, args...)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	cmd.Cancel = func() error {
		kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
		kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
		if kill.Run() != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	return cmd, nil
}

// resolveNpmShim turns an npm .cmd shim into node.exe and the script it would run.
func resolveNpmShim(shim string) (node, script string, err error) {
	data, err := os.ReadFile(shim)
	if err != nil {
		return "", "", err
	}
	m := npmShim.FindAllSubmatch(data, -1)
	if len(m) == 0 {
		return "", "", fmt.Errorf("%s is a batch file, not an npm shim; set \"binaries\" in the config to an .exe", shim)
	}
	dir := filepath.Dir(shim)
	script = filepath.Join(dir, string(m[len(m)-1][1]))
	node = filepath.Join(dir, "node.exe")
	if !isExecutable(node) {
		if node, err = FindBinary("node", ""); err != nil {
			return "", "", err
		}
	}
	if !strings.EqualFold(filepath.Ext(node), ".exe") {
		return "", "", fmt.Errorf("node.exe not found for %s", shim)
	}
	return node, script, nil
}
