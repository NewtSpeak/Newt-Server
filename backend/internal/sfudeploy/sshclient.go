package sfudeploy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// dialTimeout SSH 拨号与认证超时。
const dialTimeout = 20 * time.Second

// LogSink 逐行接收远端输出。
type LogSink func(line string)

// Target 一次 SSH 连接的目标描述。
type Target struct {
	Host     string
	Port     int
	Username string
	// ExpectedFingerprint 非空时做 TOFU 比对，不匹配即拒绝连接。
	ExpectedFingerprint string
	// TrustNewHostKey 为 true 时接受与 ExpectedFingerprint 不符的新指纹（管理员显式确认）。
	TrustNewHostKey bool
}

// Client 一条已建立的 SSH 连接。
type Client struct {
	conn *ssh.Client
	// Fingerprint 本次连接实际看到的主机公钥指纹（SHA256:...）。
	Fingerprint string
	// sudo 非 root 登录时的提权前缀参数。
	useSudo      bool
	sudoPassword string
}

// Dial 建立 SSH 连接。主机指纹按 TOFU 校验：首次记录，后续比对。
func Dial(ctx context.Context, target Target, cred Credential) (*Client, error) {
	auth, err := authMethods(cred)
	if err != nil {
		return nil, err
	}
	var seen string
	hostKeyCallback := func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		seen = ssh.FingerprintSHA256(key)
		if target.ExpectedFingerprint == "" || target.TrustNewHostKey {
			return nil
		}
		if seen != target.ExpectedFingerprint {
			return fmt.Errorf("主机密钥指纹变更（记录 %s，实际 %s），可能存在中间人攻击；确认目标机重装后可勾选「信任新指纹」",
				target.ExpectedFingerprint, seen)
		}
		return nil
	}
	port := target.Port
	if port <= 0 {
		port = 22
	}
	config := &ssh.ClientConfig{
		User:            target.Username,
		Auth:            auth,
		HostKeyCallback: hostKeyCallback,
		Timeout:         dialTimeout,
	}
	dialer := net.Dialer{Timeout: dialTimeout}
	addr := net.JoinHostPort(target.Host, strconv.Itoa(port))
	netConn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("连接 %s 失败: %w", addr, err)
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(netConn, addr, config)
	if err != nil {
		netConn.Close()
		return nil, fmt.Errorf("SSH 认证失败: %w", err)
	}
	return &Client{conn: ssh.NewClient(sshConn, chans, reqs), Fingerprint: seen}, nil
}

func authMethods(cred Credential) ([]ssh.AuthMethod, error) {
	if key := strings.TrimSpace(cred.PrivateKey); key != "" {
		var signer ssh.Signer
		var err error
		if cred.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(key), []byte(cred.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(key))
		}
		if err != nil {
			return nil, fmt.Errorf("解析 SSH 私钥失败: %w", err)
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	}
	if cred.Password != "" {
		return []ssh.AuthMethod{
			ssh.Password(cred.Password),
			// 部分服务器只开 keyboard-interactive；用同一密码应答。
			ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range answers {
					answers[i] = cred.Password
				}
				return answers, nil
			}),
		}, nil
	}
	return nil, fmt.Errorf("未提供 SSH 密码或私钥")
}

func (c *Client) Close() error { return c.conn.Close() }

// EnablePrivilegeEscalation 非 root 登录时启用 sudo 提权（sudoPassword 为空表示免密 sudo）。
func (c *Client) EnablePrivilegeEscalation(sudoPassword string) {
	c.useSudo = true
	c.sudoPassword = sudoPassword
}

// Run 执行一条命令，返回合并后的输出（不做提权，用于探测类命令）。
func (c *Client) Run(ctx context.Context, cmd string) (string, error) {
	session, err := c.conn.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()
	var out strings.Builder
	session.Stdout = &out
	session.Stderr = &out
	done := make(chan error, 1)
	go func() { done <- session.Run(cmd) }()
	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		return out.String(), ctx.Err()
	case err := <-done:
		return strings.TrimRight(out.String(), "\r\n"), err
	}
}

// RunWithStdin 执行命令并把 input 写入其 stdin，返回合并输出。
// 用于校验 sudo 密码：密码走 stdin，不进 argv、不进 shell 引号。
func (c *Client) RunWithStdin(ctx context.Context, cmd, input string) (string, error) {
	var out strings.Builder
	err := c.runStdinScript(ctx, cmd, "", input, func(line string) {
		out.WriteString(line)
		out.WriteByte('\n')
	})
	return strings.TrimRight(out.String(), "\n"), err
}

// RunScript 执行一段 bash 脚本并逐行回传输出。
//
// root 登录：脚本经 stdin 灌入 `bash -s`，不落盘、不出现在 argv。
// 非 root：先用一条无提权 session 把脚本写入 /tmp（0600），再开新 session 以
// `sudo -S -p '' bash <path>` 执行——避免 sudo 密码与脚本内容争抢同一个 stdin。
func (c *Client) RunScript(ctx context.Context, script string, sink LogSink) error {
	if !c.useSudo {
		return c.runStdinScript(ctx, "bash -s", script, "", sink)
	}
	remotePath := fmt.Sprintf("/tmp/.owl-sfu-deploy-%d.sh", time.Now().UnixNano())
	writeCmd := fmt.Sprintf("umask 077 && cat > %s", remotePath)
	if err := c.runStdinScript(ctx, writeCmd, script, "", nil); err != nil {
		return fmt.Errorf("上传部署脚本失败: %w", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = c.Run(cleanupCtx, "rm -f "+remotePath)
	}()
	// sudo -S 从 stdin 读密码；免密 sudo 时喂空行也无害。
	execCmd := fmt.Sprintf("sudo -S -p '' bash %s", remotePath)
	return c.runStdinScript(ctx, execCmd, "", c.sudoPassword+"\n", sink)
}

// runStdinScript 开一个 session 执行 cmd，把 prefix+script 写入其 stdin，并逐行回传输出。
func (c *Client) runStdinScript(ctx context.Context, cmd, script, stdinPrefix string, sink LogSink) error {
	session, err := c.conn.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()

	stdin, err := session.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		return err
	}
	if err := session.Start(cmd); err != nil {
		return err
	}

	go func() {
		if stdinPrefix != "" {
			_, _ = io.WriteString(stdin, stdinPrefix)
		}
		if script != "" {
			_, _ = io.WriteString(stdin, script)
		}
		_ = stdin.Close()
	}()

	var wg sync.WaitGroup
	var mu sync.Mutex
	emit := func(reader io.Reader) {
		defer wg.Done()
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := strings.TrimRight(scanner.Text(), "\r")
			if sink == nil {
				continue
			}
			mu.Lock()
			sink(line)
			mu.Unlock()
		}
	}
	wg.Add(2)
	go emit(stdout)
	go emit(stderr)

	done := make(chan error, 1)
	go func() {
		wg.Wait()
		done <- session.Wait()
	}()
	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		_ = session.Close()
		return ctx.Err()
	case err := <-done:
		return err
	}
}

// StreamFile 把内容写入远端文件（Server 无公网地址时上传二进制的兜底通道）。
func (c *Client) StreamFile(ctx context.Context, remotePath string, content io.Reader, mode string) error {
	cmd := fmt.Sprintf("cat > %s && chmod %s %s", remotePath, mode, remotePath)
	if c.useSudo {
		cmd = fmt.Sprintf("sudo -S -p '' sh -c %s", shellQuote(cmd))
	}
	session, err := c.conn.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	stdin, err := session.StdinPipe()
	if err != nil {
		return err
	}
	if err := session.Start(cmd); err != nil {
		return err
	}
	go func() {
		if c.useSudo {
			_, _ = io.WriteString(stdin, c.sudoPassword+"\n")
		}
		_, _ = io.Copy(stdin, content)
		_ = stdin.Close()
	}()
	done := make(chan error, 1)
	go func() { done <- session.Wait() }()
	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		return ctx.Err()
	case err := <-done:
		return err
	}
}

// shellQuote 单引号包裹并转义，用于把整条命令作为单个参数传给 sh -c。
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
