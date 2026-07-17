//go:build unix

package claudecode

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// prepareCmdForKill puts the spawned child into its own process group so that
// the entire descendant tree can be terminated with a single signal aimed at
// the negative PID. Without this, cc-connect can only signal the direct
// child (e.g. the `claude` CLI), leaving any grandchildren (MCP server
// processes such as the Telegram bridge) as orphans that may spin at 100%
// CPU when their parent disappears.
//
// Mirrors the pattern used by agent/codex/proc_unix.go.
func prepareCmdForKill(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// signalProcessGroup sends sig to the entire process group rooted at cmd.
// Returns nil if the group is already gone.
func signalProcessGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, sig); err != nil &&
		!errors.Is(err, os.ErrProcessDone) &&
		!errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

// forceKillCmd SIGKILLs the entire process group rooted at cmd. Use this
// as the last-resort escalation when graceful shutdown has timed out.
func forceKillCmd(cmd *exec.Cmd) error {
	return signalProcessGroup(cmd, syscall.SIGKILL)
}

// checkProcessState reports whether pid is still alive by sending the null
// signal (0), which performs existence/permission checks without actually
// signaling the process. If the process exists, it additionally tries to
// read /proc/<pid>/stat (Linux only) to detect a zombie state; on platforms
// without /proc (e.g. macOS/BSD) this degrades gracefully to
// processStateRunning since those kernels don't expose zombie status this
// way and processWatchdog's grace-period logic only fires on repeated
// zombie observations.
func checkProcessState(pid int) (processState, error) {
	if err := syscall.Kill(pid, 0); err != nil {
		if errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrProcessDone) {
			return processStateGone, nil
		}
		if errors.Is(err, syscall.EPERM) {
			// Process exists but we can't signal it (shouldn't happen for our
			// own child); treat as running rather than falsely reporting gone.
			return processStateRunning, nil
		}
		return processStateRunning, err
	}

	if zombie, ok := isLinuxZombie(pid); ok && zombie {
		return processStateZombie, nil
	}
	return processStateRunning, nil
}

// isLinuxZombie reads /proc/<pid>/stat and checks the process state field
// for 'Z' (zombie). The second return value is false when /proc is
// unavailable or the file can't be parsed, signaling "unknown" to the
// caller rather than a definitive answer.
func isLinuxZombie(pid int) (zombie bool, known bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false, false
	}
	// Format: "pid (comm) state ...". comm may contain spaces/parens, so
	// locate the state field after the last ')' rather than splitting naively.
	idx := strings.LastIndexByte(string(data), ')')
	if idx < 0 || idx+2 >= len(data) {
		return false, false
	}
	fields := strings.Fields(string(data[idx+1:]))
	if len(fields) == 0 {
		return false, false
	}
	return fields[0] == "Z", true
}
