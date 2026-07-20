package sfunode

import (
	"fmt"

	"github.com/google/uuid"
	"github.com/owlspeak/owl-server/backend/internal/audit"
	"github.com/owlspeak/owl-server/backend/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PoolConfig 服级节点池的完整视图。
type PoolConfig struct {
	GuildID           uuid.UUID       `json:"guild_id"`
	FallbackToDefault bool            `json:"fallback_to_default"`
	Candidates        []model.SfuNode `json:"candidates"` // 系统管理员授权的候选集
	Selected          []model.SfuNode `json:"selected"`   // 服务器管理员勾选的池成员
}

// LoadPool 读取某服节点池配置（无配置时返回默认：空候选、回落开启）。
func (s *Service) LoadPool(guildID uuid.UUID) (PoolConfig, error) {
	cfg := PoolConfig{GuildID: guildID, FallbackToDefault: true, Candidates: []model.SfuNode{}, Selected: []model.SfuNode{}}
	var pool model.SfuGuildNodePool
	if err := s.db.First(&pool, "guild_id = ?", guildID).Error; err == nil {
		cfg.FallbackToDefault = pool.FallbackToDefault
	}
	candidateIDs, err := s.poolNodeIDs(&model.SfuGuildNodeCandidate{}, guildID)
	if err != nil {
		return cfg, err
	}
	selectedIDs, err := s.poolNodeIDs(&model.SfuGuildNodeSelection{}, guildID)
	if err != nil {
		return cfg, err
	}
	if cfg.Candidates, err = s.nodesByIDs(candidateIDs); err != nil {
		return cfg, err
	}
	if cfg.Selected, err = s.nodesByIDs(selectedIDs); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (s *Service) poolNodeIDs(table any, guildID uuid.UUID) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	if err := s.db.Model(table).Where("guild_id = ?", guildID).Pluck("node_id", &ids).Error; err != nil {
		return nil, fmt.Errorf("查询节点池成员失败: %w", err)
	}
	return ids, nil
}

func (s *Service) nodesByIDs(ids []uuid.UUID) ([]model.SfuNode, error) {
	if len(ids) == 0 {
		return []model.SfuNode{}, nil
	}
	var nodes []model.SfuNode
	if err := s.db.Where("id IN ?", ids).Order("created_at ASC").Find(&nodes).Error; err != nil {
		return nil, fmt.Errorf("查询节点失败: %w", err)
	}
	return nodes, nil
}

// SetAdminPool 系统管理员覆盖某服节点池：授权候选集、（可选）直接指定勾选集、回落开关。
// 候选集收缩时自动剔除已不在候选内的勾选（docs 07 专项 2 注意 2 的删节点场景由调用方触发 Drain）。
func (s *Service) SetAdminPool(actor model.User, guildID uuid.UUID, candidateIDs []uuid.UUID, selectedIDs *[]uuid.UUID, fallback *bool) (PoolConfig, error) {
	if err := s.validateNodeIDs(candidateIDs); err != nil {
		return PoolConfig{}, err
	}
	candidateSet := toSet(candidateIDs)
	if selectedIDs != nil {
		for _, id := range *selectedIDs {
			if !candidateSet[id] {
				return PoolConfig{}, fmt.Errorf("勾选节点 %s 不在候选集内", id)
			}
		}
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		// upsert 池配置行。
		pool := model.SfuGuildNodePool{GuildID: guildID, FallbackToDefault: true}
		if fallback != nil {
			pool.FallbackToDefault = *fallback
		}
		assign := map[string]any{}
		if fallback != nil {
			assign["fallback_to_default"] = *fallback
		}
		conflict := clause.OnConflict{Columns: []clause.Column{{Name: "guild_id"}}, DoNothing: len(assign) == 0}
		if len(assign) > 0 {
			conflict.DoUpdates = clause.Assignments(assign)
		}
		if err := tx.Clauses(conflict).Create(&pool).Error; err != nil {
			return fmt.Errorf("保存节点池配置失败: %w", err)
		}
		// 全量替换候选集。
		if err := tx.Where("guild_id = ?", guildID).Delete(&model.SfuGuildNodeCandidate{}).Error; err != nil {
			return err
		}
		for _, id := range candidateIDs {
			if err := tx.Create(&model.SfuGuildNodeCandidate{GuildID: guildID, NodeID: id}).Error; err != nil {
				return fmt.Errorf("保存候选节点失败: %w", err)
			}
		}
		if selectedIDs != nil {
			// 系统管理员直接覆盖勾选集。
			if err := tx.Where("guild_id = ?", guildID).Delete(&model.SfuGuildNodeSelection{}).Error; err != nil {
				return err
			}
			for _, id := range *selectedIDs {
				if err := tx.Create(&model.SfuGuildNodeSelection{GuildID: guildID, NodeID: id}).Error; err != nil {
					return fmt.Errorf("保存勾选节点失败: %w", err)
				}
			}
		} else {
			// 候选收缩：剔除不再被授权的勾选。
			if err := tx.Where("guild_id = ? AND node_id NOT IN (SELECT node_id FROM sfu_guild_node_candidates WHERE guild_id = ?)",
				guildID, guildID).Delete(&model.SfuGuildNodeSelection{}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return PoolConfig{}, err
	}
	audit.Log(s.db, audit.Entry{
		ActorID: &actor.ID, ActorType: "system_admin", GuildID: &guildID,
		Action: "sfu_pool.admin_update", TargetType: "guild_node_pool", TargetID: guildID.String(),
		Detail: map[string]any{"candidates": uuidStrings(candidateIDs), "selected_override": selectedIDs != nil, "fallback": fallback},
	})
	return s.LoadPool(guildID)
}

// SetGuildPool 服务器管理员从系统管授权候选集中勾选本服节点池（docs 07 专项 2.1）。
func (s *Service) SetGuildPool(actor model.User, guildID uuid.UUID, nodeIDs []uuid.UUID, fallback *bool) (PoolConfig, error) {
	candidateIDs, err := s.poolNodeIDs(&model.SfuGuildNodeCandidate{}, guildID)
	if err != nil {
		return PoolConfig{}, err
	}
	candidateSet := toSet(candidateIDs)
	for _, id := range nodeIDs {
		if !candidateSet[id] {
			return PoolConfig{}, fmt.Errorf("节点 %s 不在本服被授权的候选集内", id)
		}
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		pool := model.SfuGuildNodePool{GuildID: guildID, FallbackToDefault: true}
		if fallback != nil {
			pool.FallbackToDefault = *fallback
		}
		conflict := clause.OnConflict{Columns: []clause.Column{{Name: "guild_id"}}, DoNothing: fallback == nil}
		if fallback != nil {
			conflict.DoUpdates = clause.Assignments(map[string]any{"fallback_to_default": *fallback})
		}
		if err := tx.Clauses(conflict).Create(&pool).Error; err != nil {
			return fmt.Errorf("保存节点池配置失败: %w", err)
		}
		if err := tx.Where("guild_id = ?", guildID).Delete(&model.SfuGuildNodeSelection{}).Error; err != nil {
			return err
		}
		for _, id := range nodeIDs {
			if err := tx.Create(&model.SfuGuildNodeSelection{GuildID: guildID, NodeID: id}).Error; err != nil {
				return fmt.Errorf("保存勾选节点失败: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return PoolConfig{}, err
	}
	audit.Log(s.db, audit.Entry{
		ActorID: &actor.ID, ActorType: "guild_admin", GuildID: &guildID,
		Action: "sfu_pool.guild_update", TargetType: "guild_node_pool", TargetID: guildID.String(),
		Detail: map[string]any{"selected": uuidStrings(nodeIDs), "fallback": fallback},
	})
	return s.LoadPool(guildID)
}

// validateNodeIDs 候选节点必须存在且未吊销。
func (s *Service) validateNodeIDs(ids []uuid.UUID) error {
	for _, id := range ids {
		var node model.SfuNode
		if err := s.db.First(&node, "id = ?", id).Error; err != nil {
			return fmt.Errorf("节点 %s 不存在", id)
		}
		if node.Status == model.SfuNodeRevoked {
			return fmt.Errorf("节点 %s 已被吊销，不能加入节点池", id)
		}
	}
	return nil
}

func toSet(ids []uuid.UUID) map[uuid.UUID]bool {
	set := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}

func uuidStrings(ids []uuid.UUID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, id.String())
	}
	return out
}
