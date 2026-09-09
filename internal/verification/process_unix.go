//go:build linux || darwin || freebsd

package verification

import (
	"os"
	"os/exec"
	"syscall"
)

func terminationScope() string { return "unix-process-group" }
func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
