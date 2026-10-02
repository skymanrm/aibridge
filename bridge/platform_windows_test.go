package bridge

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveNpmShim(t *testing.T) {
	dir := t.TempDir()
	shim := filepath.Join(dir, "codex.cmd")
	body := "@ECHO off\r\nGOTO start\r\n:find_dp0\r\nSET dp0=%~dp0\r\nEXIT /b\r\n:start\r\nSETLOCAL\r\nCALL :find_dp0\r\n\r\n" +
		"IF EXIST \"%dp0%\\node.exe\" (\r\n  SET \"_prog=%dp0%\\node.exe\"\r\n) ELSE (\r\n  SET \"_prog=node\"\r\n)\r\n\r\n" +
		"endLocal & goto #_undefined_# 2>NUL || title %COMSPEC% & \"%_prog%\"  \"%dp0%\\node_modules\\@openai\\codex\\bin\\codex.js\" %*\r\n"
	if err := os.WriteFile(shim, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "node.exe"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	node, script, err := resolveNpmShim(shim)
	if err != nil {
		t.Fatal(err)
	}
	if node != filepath.Join(dir, "node.exe") || script != filepath.Join(dir, "node_modules", "@openai", "codex", "bin", "codex.js") {
		t.Errorf("got node=%s script=%s", node, script)
	}

	bat := filepath.Join(dir, "other.bat")
	if err := os.WriteFile(bat, []byte("@echo %*\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveNpmShim(bat); err == nil {
		t.Error("plain batch file accepted")
	}
}
