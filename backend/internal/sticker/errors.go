package sticker

import "errors"

var (
	errUnsupportedMIME = errors.New("仅支持 PNG/JPEG/WebP/GIF")
	errFileTooLarge    = errors.New("文件超过大小上限")
	errInvalidImage    = errors.New("无法解析图片尺寸")
	errPackLimit       = errors.New("自建贴图包数量已达上限")
	errItemLimit       = errors.New("包内条目数量已达上限")
	errKindMismatch    = errors.New("条目 kind 必须与包一致")
	errNotOwner        = errors.New("仅包所有者可操作")
	errPackNotActive   = errors.New("包当前状态不允许此操作")
	errCannotCopy      = errors.New("该包不允许单条复制")
	errCannotInstall   = errors.New("该包不允许整包安装")
	errGuildScope      = errors.New("服独属包仅可在所属服使用")
	errNotAvailable    = errors.New("表情不在可用集合内")
	errRestoreExpired  = errors.New("已超过 180 天恢复期限")
	errAlreadyBanned   = errors.New("该包已在本服被 ban")
)
