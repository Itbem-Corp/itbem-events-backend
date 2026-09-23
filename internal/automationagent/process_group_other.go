//go:build windows || plan9 || js

package automationagent

import "os/exec"

// Windows/Plan 9 do not expose the POSIX process-group contract used by the
// Linux worker. CommandContext still terminates the direct command, and the
// deployment contract requires a real container/VM for hostile repositories.
func configureCommandProcessGroup(_ *exec.Cmd) func() { return func() {} }
