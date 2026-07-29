package auditundo

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/newtspeak/newt-server/backend/internal/audit"
	"github.com/newtspeak/newt-server/backend/internal/model"
)

func stateOf(log model.AuditLog) (before, after, detail map[string]any) {
	before = audit.ParseState(log.BeforeState)
	after = audit.ParseState(log.AfterState)
	detail = audit.ParseDetail(log.Detail)
	// 兼容：旧记录 before 只在 detail 里
	if len(before) == 0 {
		if m, ok := detail["before"].(map[string]any); ok {
			before = m
		}
	}
	if len(after) == 0 {
		if m, ok := detail["after"].(map[string]any); ok {
			after = m
		}
	}
	return before, after, detail
}

func strField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			switch t := v.(type) {
			case string:
				if t != "" {
					return t
				}
			case fmt.Stringer:
				return t.String()
			default:
				// json number for uuid unlikely; use fmt
				s := fmt.Sprint(t)
				if s != "" && s != "<nil>" {
					return s
				}
			}
		}
	}
	return ""
}

func uuidField(m map[string]any, keys ...string) (uuid.UUID, bool) {
	s := strField(m, keys...)
	if s == "" {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

func int64Field(m map[string]any, keys ...string) (int64, bool) {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			switch t := v.(type) {
			case float64:
				return int64(t), true
			case int64:
				return t, true
			case int:
				return int64(t), true
			case json.Number:
				n, err := t.Int64()
				return n, err == nil
			case string:
				n, err := strconv.ParseInt(t, 10, 64)
				return n, err == nil
			}
		}
	}
	return 0, false
}

func boolField(m map[string]any, keys ...string) (bool, bool) {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			if b, ok := v.(bool); ok {
				return b, true
			}
		}
	}
	return false, false
}

func targetGone(msg string) error {
	if msg == "" {
		msg = "目标已不存在，无法撤销"
	}
	return errf(http.StatusConflict, "TARGET_GONE", msg)
}

func badState(msg string) error {
	if msg == "" {
		msg = "审计快照不完整，无法撤销"
	}
	return errf(http.StatusConflict, "INCOMPLETE_SNAPSHOT", msg)
}
