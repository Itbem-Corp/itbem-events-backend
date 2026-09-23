//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly || solaris

package automationagent

import (
	"os/exec"
	"syscall"
)

// configureCommandProcessGroup isolates a repository command's descendants
// from the worker process. It is intentionally a tiny OS adapter so the
// command runner remains testable and the Windows development worker keeps a
// safe, compiling fallback.
func configureCommandProcessGroup(command *exec.Cmd) func() {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return func() {
		if command.Process == nil {
			return
		}
		// A negative PID addresses the process group created above. Ignore ESRCH:
		// the group may already have exited between CommandContext cancellation
		// and this cleanup call.
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
}
