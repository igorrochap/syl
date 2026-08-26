//go:build !aix && !darwin && !dragonfly && !freebsd && !illumos && !linux && !netbsd && !openbsd && !solaris

package orchestration

import "os/exec"

func configureSetupProcess(*exec.Cmd) {}

func killSetupProcess(process *exec.Cmd) error {
	if process.Process == nil {
		return nil
	}
	return process.Process.Kill()
}
