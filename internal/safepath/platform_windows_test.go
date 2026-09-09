package safepath

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWindowsJunctionEscape(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir()
	junction := filepath.Join(root, ".harness")
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", `New-Item -ItemType Junction -Path $env:HARNESS_JUNCTION -Target $env:HARNESS_TARGET -ErrorAction Stop | Out-Null`)
	cmd.Env = append(os.Environ(), "HARNESS_JUNCTION="+junction, "HARNESS_TARGET="+target)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	defer os.Remove(junction)
	if err := EnsureDirectory(root, ".harness/leases"); err == nil {
		t.Fatal("state directory traversed junction")
	}
	if _, err := os.Stat(filepath.Join(target, "leases")); !os.IsNotExist(err) {
		t.Fatal("mutated external target before admission")
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := Check(r, ".harness/missing", true); err == nil {
		t.Fatal("junction traversal admitted")
	}
}
