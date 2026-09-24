package logstore

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// SliceLogMessages 按 object_type 归一化日志的消息序列并返回分页切片与总数。
//
// 消息序列组成：
//   - chat 类（chat.completion / chat.completion.chunk）：InputHistoryParsed + OutputMessageParsed
//   - responses 类（response / responses / responses_stream）：ResponsesInputHistoryParsed + ResponsesOutputParsed
//   - 其他 object 类型：空序列
//
// 分页语义：
//   - offset < 0 归一化为 0；offset >= total 返回空切片、总数与 nil error（不视为错误）
//   - limit <= 0 视为非法输入返回 error，默认值由调用方决定，本函数不做兜底
func SliceLogMessages(l *Log, offset, limit int) ([]json.RawMessage, int, error) {
	return sliceLogMessages(l, offset, limit, false, nil)
}

// SliceLogMessagesLatestFirst returns the same message sequence with the newest message first.
func SliceLogMessagesLatestFirst(l *Log, offset, limit int) ([]json.RawMessage, int, error) {
	return sliceLogMessages(l, offset, limit, true, nil)
}

// SliceLogMessagesWithRoles 按角色过滤后返回分页切片与过滤后总数。
// roles 为 nil 或空表示不过滤（与 SliceLogMessages 完全一致）。
func SliceLogMessagesWithRoles(l *Log, offset, limit int, roles []string) ([]json.RawMessage, int, error) {
	return sliceLogMessages(l, offset, limit, false, roles)
}

// SliceLogMessagesLatestFirstWithRoles 返回按角色过滤后的最新优先分页。
func SliceLogMessagesLatestFirstWithRoles(l *Log, offset, limit int, roles []string) ([]json.RawMessage, int, error) {
	return sliceLogMessages(l, offset, limit, true, roles)
}

func sliceLogMessages(l *Log, offset, limit int, latestFirst bool, roles []string) ([]json.RawMessage, int, error) {
	if l == nil {
		return nil, 0, fmt.Errorf("logstore: log is nil")
	}
	if limit <= 0 {
		return nil, 0, fmt.Errorf("logstore: limit must be positive, got %d", limit)
	}
	if offset < 0 {
		offset = 0
	}

	var messages []any
	switch l.Object {
	case "chat.completion", "chat.completion.chunk":
		for i := range l.InputHistoryParsed {
			messages = append(messages, &l.InputHistoryParsed[i])
		}
		if l.OutputMessageParsed != nil {
			messages = append(messages, l.OutputMessageParsed)
		}
	case "response", "responses", "responses_stream":
		for i := range l.ResponsesInputHistoryParsed {
			messages = append(messages, &l.ResponsesInputHistoryParsed[i])
		}
		for i := range l.ResponsesOutputParsed {
			messages = append(messages, &l.ResponsesOutputParsed[i])
		}
	default:
		// 其他模态（embedding、speech 等）不属于本端点的消息语义
		return []json.RawMessage{}, 0, nil
	}

	if len(roles) > 0 {
		allow := make(map[string]struct{}, len(roles))
		for _, r := range roles {
			allow[r] = struct{}{}
		}
		kept := messages[:0]
		for _, msg := range messages {
			if _, ok := allow[messageRole(msg)]; ok {
				kept = append(kept, msg)
			}
		}
		messages = kept
	}

	total := len(messages)
	if latestFirst {
		for left, right := 0, total-1; left < right; left, right = left+1, right-1 {
			messages[left], messages[right] = messages[right], messages[left]
		}
	}
	if offset >= total {
		return []json.RawMessage{}, total, nil
	}
	end := min(offset+limit, total)

	result := make([]json.RawMessage, 0, end-offset)
	for _, msg := range messages[offset:end] {
		data, err := sonic.Marshal(msg)
		if err != nil {
			return nil, total, fmt.Errorf("logstore: failed to marshal message: %w", err)
		}
		result = append(result, json.RawMessage(data))
	}
	return result, total, nil
}

func messageRole(msg any) string {
	switch m := msg.(type) {
	case *schemas.ChatMessage:
		return chatMessageRole(m)
	case schemas.ChatMessage:
		return chatMessageRole(&m)
	case *schemas.ResponsesMessage:
		return responsesMessageRole(m)
	case schemas.ResponsesMessage:
		return responsesMessageRole(&m)
	default:
		return "assistant"
	}
}

func chatMessageRole(m *schemas.ChatMessage) string {
	if m == nil {
		return "assistant"
	}
	switch string(m.Role) {
	case "user":
		return "user"
	case "assistant":
		return "assistant"
	case "system", "developer":
		return "system"
	case "tool":
		return "tool"
	default:
		return "assistant"
	}
}

func responsesMessageRole(m *schemas.ResponsesMessage) string {
	if m == nil {
		return "assistant"
	}
	if m.Type != nil {
		t := string(*m.Type)
		if t == "reasoning" {
			return "reasoning"
		}
		if strings.HasSuffix(t, "_call") || strings.HasSuffix(t, "_call_output") ||
			t == "tool_search_output" || t == "additional_tools" ||
			t == "mcp_list_tools" || t == "mcp_approval_request" ||
			t == "mcp_approval_responses" {
			return "tool"
		}
	}
	if m.Role == nil {
		return "assistant"
	}
	switch string(*m.Role) {
	case "user":
		return "user"
	case "assistant":
		return "assistant"
	case "system", "developer":
		return "system"
	default:
		return "assistant"
	}
}
