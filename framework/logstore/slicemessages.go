package logstore

import (
	"encoding/json"
	"fmt"

	"github.com/bytedance/sonic"
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

	total := len(messages)
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
