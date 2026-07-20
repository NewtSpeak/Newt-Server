package model

var registry []any

// Register 登记需要自动迁移的模型。
// 各领域模型文件（models_*.go）在自己的 init 中调用，避免并行开发时争抢同一个清单函数。
func Register(models ...any) { registry = append(registry, models...) }

// Models 返回全部已登记模型，供 database.Open 执行 AutoMigrate。
func Models() []any { return registry }
