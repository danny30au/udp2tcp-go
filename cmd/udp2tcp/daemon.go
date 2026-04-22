package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

// daemonizedEnv is set in the child process so it knows not to re-fork.
const daemonizedEnv = "UDP2TCP_DAEMONIZED"

// daemonize re-executes the current process with stdio detached and exits
// the parent. The child sees daemonizedEnv=1 and continues normally. When
// pidFile is non-empty, the parent writes the child's PID to it before exit.
//
// Returns true in the parent (which will then exit), false in the child.
func daemonize(pidFile string) (bool, error) {
	if os.Getenv(daemonizedEnv) == "1" {
		// Already the detached child — nothing to do.
		return false, nil
	}

	exe, err := os.Executable()
	if err != nil {
		return false, fmt.Errorf("daemonize: locate executable: %w", err)
	}

	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return false, fmt.Errorf("daemonize: open %s: %w", os.DevNull, err)
	}
	defer devNull.Close()

	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Env = append(os.Environ(), daemonizedEnv+"=1")
	cmd.Stdin = devNull
	cmd.Stdout = devNull
	cmd.Stderr = devNull
	cmd.SysProcAttr = sysProcAttrDetached()

	if err := cmd.Start(); err != nil {
		return false, fmt.Errorf("daemonize: start child: %w", err)
	}

	if pidFile != "" {
		if err := writePIDFile(pidFile, cmd.Process.Pid); err != nil {
			// Best-effort: kill the child so we don't leave an orphan
			// running without a PID file the operator expected.
			_ = cmd.Process.Kill()
			return false, fmt.Errorf("daemonize: write pidfile: %w", err)
		}
	}

	// Detach: don't wait for the child.
	_ = cmd.Process.Release()
	return true, nil
}

// writePIDFile atomically writes pid to path with 0644 permissions.
func writePIDFile(path string, pid int) error {
	tmp, err := os.CreateTemp(dirOf(path), ".pid-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(strconv.Itoa(pid) + "\n"); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

// removePIDFile deletes the PID file if it still contains our PID.
func removePIDFile(path string) {
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(trimSpace(string(data)))
	if err != nil || pid != os.Getpid() {
		return
	}
	_ = os.Remove(path)
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			if i == 0 {
				return string(path[0])
			}
			return path[:i]
		}
	}
	return "."
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && isSpace(s[start]) {
		start++
	}
	for end > start && isSpace(s[end-1]) {
		end--
	}
	return s[start:end]
}

func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}
