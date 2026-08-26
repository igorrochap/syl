//go:build aix || darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd || solaris

package orchestration

import (
	"os/exec"
	"syscall"
)

func configureSetupProcess(process *exec.Cmd) {
	process.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killSetupProcess(process *exec.Cmd) error {
	if process.Process == nil {
		return nil
	}
	if err := syscall.Kill(-process.Process.Pid, syscall.SIGKILL); err == nil {
		return nil
	}
	return process.Process.Kill()
}
