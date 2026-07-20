package model

import "time"

// SfuNode 定义见 models_sfunode.go（节点管理专项）；本文件只登记集群密钥表。

// ClusterSecret 集群级密钥材料（内嵌 CA、Media Token 签名密钥），保证多次重启一致。
type ClusterSecret struct {
	Name      string `gorm:"size:64;primaryKey"`
	Value     string `gorm:"type:text;not null"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

func init() { Register(&ClusterSecret{}) }
