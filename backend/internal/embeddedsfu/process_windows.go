//go:build windows

package embeddedsfu

import (
	"os"
	"os/exec"
	"time"
)

func setChildProcGroup(cmd *exec.Cmd) {
	// Windows 无进程组 Setpgid；子进程随 CreateProcess 默认行为即可。
}

func stopProcess(cmd *exec.Cmd, grace time.Duration) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	done := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(grace):
		_ = cmd.Process.Kill()
		<-done
	}
}

func processAlive(pid int) bool {
	// Windows 上 Signal(0) 不可靠；尝试 OpenProcess 等价于 FindProcess 成功不代表存活。
	// 用短暂 Wait 不可行；这里保守认为 pid 文件存在即尝试 Kill。
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// FindProcess on Windows always succeeds; try Kill with 0-timeout by checking
	// whether we can signal — Process.Kill is the only practical approach later.
	_ = proc
	return true
}

func killPID(pid int, grace time.Duration) {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	_ = proc.Kill()
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
}
