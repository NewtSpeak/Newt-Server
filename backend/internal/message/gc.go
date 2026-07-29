package message

import (
	"log"
	"time"

	"github.com/newtspeak/newt-server/backend/internal/model"
)

// 后台清理任务（docs 13 AT.8 / 上传时序注 / AW）：
//   - 未绑定消息的附件：创建 24h 后清理（防垃圾上传占盘）；
//   - 软删消息的附件：删除 7 天后延迟 GC（保留窗口内审计可取证）；
//   - 消息保留策略：服级 retention_days > 0 时，到期消息每小时硬删
//     （连带编辑历史、反应、附件；索引随行删除自然出库）。

const (
	gcInterval           = time.Hour
	unboundAttachmentTTL = 24 * time.Hour
	deletedAttachmentTTL = 7 * 24 * time.Hour
	gcBatchSize          = 500
)

// startGC 启动清理循环：启动后先跑一轮，之后每小时一轮。
func (s *service) startGC() {
	go func() {
		s.runGCOnce()
		ticker := time.NewTicker(gcInterval)
		defer ticker.Stop()
		for range ticker.C {
			s.runGCOnce()
		}
	}()
}

func (s *service) runGCOnce() {
	now := time.Now().UTC()
	s.gcUnboundAttachments(now)
	s.gcDeletedMessageAttachments(now)
	s.gcStaleStreams(now)
	s.gcExpiredInteractions(now)
	s.applyRetention(now)
}

// gcUnboundAttachments 清理超过 24h 仍未绑定任何消息的附件（含 presign 后从未上传的记录）。
func (s *service) gcUnboundAttachments(now time.Time) {
	var attachments []model.Attachment
	err := s.db.Where("message_id IS NULL AND created_at < ?", now.Add(-unboundAttachmentTTL)).
		Limit(gcBatchSize).Find(&attachments).Error
	if err != nil {
		log.Printf("message: 扫描未绑定附件失败 err=%v", err)
		return
	}
	s.deleteAttachments(attachments, "unbound")
}

// gcDeletedMessageAttachments 清理软删超过 7 天的消息所挂的附件（消息行保留为墓碑）。
func (s *service) gcDeletedMessageAttachments(now time.Time) {
	var attachments []model.Attachment
	err := s.db.
		Joins("JOIN messages ON messages.id = attachments.message_id").
		Where("messages.deleted_at IS NOT NULL AND messages.deleted_at < ?", now.Add(-deletedAttachmentTTL)).
		Limit(gcBatchSize).Find(&attachments).Error
	if err != nil {
		log.Printf("message: 扫描软删消息附件失败 err=%v", err)
		return
	}
	s.deleteAttachments(attachments, "deleted_message")
}

// deleteAttachments 先删存储对象再删元数据行；存储删除幂等，失败下轮重试。
func (s *service) deleteAttachments(attachments []model.Attachment, reason string) {
	for _, attachment := range attachments {
		if err := s.storage.Delete(attachment.ObjectKey); err != nil {
			log.Printf("message: 删除附件对象失败 id=%s reason=%s err=%v", attachment.ID, reason, err)
			continue
		}
		if err := s.db.Delete(&model.Attachment{}, "id = ?", attachment.ID).Error; err != nil {
			log.Printf("message: 删除附件记录失败 id=%s err=%v", attachment.ID, err)
		}
	}
}

// applyRetention 执行消息保留策略：逐服扫描到期消息并硬删（AW）。
func (s *service) applyRetention(now time.Time) {
	var configs []model.GuildMessageConfig
	if err := s.db.Where("retention_days > 0").Find(&configs).Error; err != nil {
		log.Printf("message: 读取保留策略失败 err=%v", err)
		return
	}
	for _, config := range configs {
		cutoff := now.AddDate(0, 0, -config.RetentionDays)
		for {
			var ids []int64
			err := s.db.Model(&model.Message{}).
				Where("guild_id = ? AND created_at < ?", config.GuildID, cutoff).
				Limit(gcBatchSize).Pluck("id", &ids).Error
			if err != nil || len(ids) == 0 {
				if err != nil {
					log.Printf("message: 扫描到期消息失败 guild=%s err=%v", config.GuildID, err)
				}
				break
			}
			// 附件需要先删存储对象。
			var attachments []model.Attachment
			if err := s.db.Where("message_id IN ?", ids).Find(&attachments).Error; err == nil {
				s.deleteAttachments(attachments, "retention")
			}
			if err := s.db.Where("message_id IN ?", ids).Delete(&model.MessageEdit{}).Error; err != nil {
				log.Printf("message: 删除编辑历史失败 err=%v", err)
			}
			if err := s.db.Where("message_id IN ?", ids).Delete(&model.MessageReaction{}).Error; err != nil {
				log.Printf("message: 删除反应失败 err=%v", err)
			}
			if err := s.db.Where("id IN ?", ids).Delete(&model.Message{}).Error; err != nil {
				log.Printf("message: 硬删到期消息失败 guild=%s err=%v", config.GuildID, err)
				break
			}
			if len(ids) < gcBatchSize {
				break
			}
		}
	}
}
