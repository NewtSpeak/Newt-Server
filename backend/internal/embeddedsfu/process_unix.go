//go:build unix

package embeddedsfu

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

func setChildProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func stopProcess(cmd *exec.Cmd, grace time.Duration) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err == nil {
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
	} else {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
	done := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(grace):
		if err == nil {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		} else {
			_ = cmd.Process.Kill()
		}
		<-done
	}
}

func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

func killPID(pid int, grace time.Duration) {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	pgid, gerr := syscall.Getpgid(pid)
	if gerr == nil {
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
	} else {
		_ = proc.Signal(syscall.SIGTERM)
	}
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	if processAlive(pid) {
		if gerr == nil {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		} else {
			_ = proc.Kill()
		}
	}
}
