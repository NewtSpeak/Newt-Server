package message

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Storage 附件二进制存储抽象（docs 13 AT.1）。
// 首期提供本地磁盘实现；日后切换 S3 等对象存储时只需替换实现并把
// presign 返回的 upload_url 换成真正的对象存储预签名地址，API 契约不变。
type Storage interface {
	// Save 将 r 写入 objectKey，最多读取 maxSize 字节，返回实际写入字节数。
	// 超过 maxSize 返回 errObjectTooLarge 并保证不留残留文件。
	Save(objectKey string, r io.Reader, maxSize int64) (int64, error)
	// Open 打开对象用于下载。
	Open(objectKey string) (io.ReadSeekCloser, error)
	// Delete 删除对象；对象不存在视为成功（GC 幂等）。
	Delete(objectKey string) error
}

var errObjectTooLarge = errors.New("附件内容超过声明大小")

// localStorage 本地磁盘实现：文件存放在 <root>/（root = cfg.DataDir/attachments）。
// objectKey 使用附件 UUID，按前两位十六进制分桶避免单目录文件过多。
type localStorage struct {
	root string
}

func newLocalStorage(root string) (*localStorage, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("创建附件存储目录: %w", err)
	}
	return &localStorage{root: root}, nil
}

// path objectKey → 磁盘路径；拒绝路径穿越字符（objectKey 由服务端生成，双保险）。
func (s *localStorage) path(objectKey string) (string, error) {
	if objectKey == "" || strings.ContainsAny(objectKey, `/\`) || strings.Contains(objectKey, "..") {
		return "", fmt.Errorf("非法对象键: %q", objectKey)
	}
	bucket := "00"
	if len(objectKey) >= 2 {
		bucket = objectKey[:2]
	}
	return filepath.Join(s.root, bucket, objectKey), nil
}

func (s *localStorage) Save(objectKey string, r io.Reader, maxSize int64) (int64, error) {
	target, err := s.path(objectKey)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return 0, err
	}
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, err
	}
	// 多读 1 字节以检测超限。
	written, err := io.Copy(file, io.LimitReader(r, maxSize+1))
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil && written > maxSize {
		err = errObjectTooLarge
	}
	if err != nil {
		_ = os.Remove(target)
		return 0, err
	}
	return written, nil
}

func (s *localStorage) Open(objectKey string) (io.ReadSeekCloser, error) {
	target, err := s.path(objectKey)
	if err != nil {
		return nil, err
	}
	return os.Open(target)
}

func (s *localStorage) Delete(objectKey string) error {
	target, err := s.path(objectKey)
	if err != nil {
		return err
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
