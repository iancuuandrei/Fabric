package verification

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

func terminationScope() string { return "windows-best-effort-process-tree" }
func configureProcess(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		path := filepath.Join(os.Getenv("SystemRoot"), "System32", "taskkill.exe")
		if !filepath.IsAbs(path) {
			return cmd.Process.Kill()
		}
		kill := exec.CommandContext(ctx, path, "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F")
		_ = kill.Run()
		return cmd.Process.Kill()
	}
}
