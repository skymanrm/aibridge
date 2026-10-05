//go:build !windows

package bridge

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestParseShellPath(t *testing.T) {
	out := "Welcome!\nPATH=/wrong\n" + pathMarker + "\nHOME=/Users/x\nPATH=/Users/x/.nvm/versions/node/v22.1.0/bin:/usr/bin\n"
	if got := parseShellPath(out); !slices.Equal(got, []string{"/Users/x/.nvm/versions/node/v22.1.0/bin", "/usr/bin"}) {
		t.Errorf("got %v", got)
	}
	if got := parseShellPath("zsh: no such file"); got != nil {
		t.Errorf("no marker: %v", got)
	}
}

func TestNodeVersionDirsNewestFirst(t *testing.T) {
	root := t.TempDir()
	for _, v := range []string{"v9.11.2", "v22.1.0", "v18.20.4", "v22.10.0"} {
		if err := os.MkdirAll(filepath.Join(root, v, "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got := nodeVersionDirs(root)
	want := []string{"v22.10.0", "v22.1.0", "v18.20.4", "v9.11.2"}
	for i, v := range want {
		if got[i] != filepath.Join(root, v, "bin") {
			t.Fatalf("got %v", got)
		}
	}
}

func TestMergePath(t *testing.T) {
	if got := mergePath([]string{"/a", "", "/b", "/a"}); got != "/a:/b" {
		t.Errorf("got %q", got)
	}
}
