// Package secretstore 抽象 ClusterSecret 的读写，供 internal/ca 与 internal/mediatoken 复用；
// 生产实现落 PostgreSQL（GormStore），单元测试用 MemoryStore。
package secretstore

import (
	"errors"
	"sync"

	"github.com/owlspeak/owl-server/backend/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Store 集群密钥的读写接口。
type Store interface {
	// Get 返回密钥值；不存在时 ok=false 且 err=nil。
	Get(name string) (value string, ok bool, err error)
	// Set 幂等写入（存在则覆盖）。
	Set(name, value string) error
}

// GormStore 基于 ClusterSecret 表的实现。
type GormStore struct{ DB *gorm.DB }

func (s GormStore) Get(name string) (string, bool, error) {
	var secret model.ClusterSecret
	err := s.DB.First(&secret, "name = ?", name).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return secret.Value, true, nil
}

func (s GormStore) Set(name, value string) error {
	secret := model.ClusterSecret{Name: name, Value: value}
	return s.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "name"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
	}).Create(&secret).Error
}

// MemoryStore 内存实现（测试用）。
type MemoryStore struct {
	mu     sync.Mutex
	values map[string]string
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{values: map[string]string{}} }

func (s *MemoryStore) Get(name string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[name]
	return value, ok, nil
}

func (s *MemoryStore) Set(name, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[name] = value
	return nil
}
