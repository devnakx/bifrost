package logstore

import (
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// chatLog 构造 3 条输入 + 1 条输出的 chat 日志，正文用唯一编号避免期望值歧义
func chatLog() *Log {
	return &Log{
		Object: "chat.completion",
		InputHistoryParsed: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: ptrStr("user-0")}},
			{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: ptrStr("assistant-1")}},
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: ptrStr("user-2")}},
		},
		OutputMessageParsed: &schemas.ChatMessage{
			Role:    schemas.ChatMessageRoleAssistant,
			Content: &schemas.ChatMessageContent{ContentStr: ptrStr("output-3")},
		},
	}
}

// responsesLog 构造 2 条输入 + 2 条输出的 responses 日志
func responsesLog() *Log {
	msgTypeIn := schemas.ResponsesMessageTypeMessage
	roleUser := schemas.ResponsesInputMessageRoleUser
	roleAssistant := schemas.ResponsesInputMessageRoleAssistant
	return &Log{
		Object: "responses",
		ResponsesInputHistoryParsed: []schemas.ResponsesMessage{
			{Type: &msgTypeIn, Role: &roleUser, Content: &schemas.ResponsesMessageContent{ContentStr: ptrStr("rin-0")}},
			{Type: &msgTypeIn, Role: &roleUser, Content: &schemas.ResponsesMessageContent{ContentStr: ptrStr("rin-1")}},
		},
		ResponsesOutputParsed: []schemas.ResponsesMessage{
			{Type: &msgTypeIn, Role: &roleAssistant},
			{Type: &msgTypeIn, Role: &roleAssistant},
		},
	}
}

func ptrStr(s string) *string { return &s }

func TestSliceLogMessages(t *testing.T) {
	tests := []struct {
		name       string
		log        func() *Log
		offset     int
		limit      int
		wantCount  int
		wantTotal  int
		wantErr    bool
		wantBodies []string // 逐条包含的正文子串（nil 表示不校验内容）
	}{
		{
			name:       "chat 全量",
			log:        chatLog,
			offset:     0,
			limit:      10,
			wantCount:  4,
			wantTotal:  4,
			wantBodies: []string{"user-0", "assistant-1", "user-2", "output-3"},
		},
		{
			name:       "responses 全量含输入与输出",
			log:        responsesLog,
			offset:     0,
			limit:      10,
			wantCount:  4,
			wantTotal:  4,
			wantBodies: []string{"rin-0"},
		},
		{
			name:      "chat 中段分页",
			log:       chatLog,
			offset:    1,
			limit:     2,
			wantCount: 2,
			wantTotal: 4,
		},
		{
			name:      "其他 object 类型返回空序列",
			log:       func() *Log { return &Log{Object: "embedding"} },
			offset:    0,
			limit:     10,
			wantCount: 0,
			wantTotal: 0,
		},
		{
			name:      "空日志返回空序列",
			log:       func() *Log { return &Log{Object: "chat.completion"} },
			offset:    0,
			limit:     10,
			wantCount: 0,
			wantTotal: 0,
		},
		{
			name:      "offset 越界返回空切片加总数无错误",
			log:       chatLog,
			offset:    100,
			limit:     10,
			wantCount: 0,
			wantTotal: 4,
		},
		{
			name:      "负 offset 归一化为 0",
			log:       chatLog,
			offset:    -5,
			limit:     2,
			wantCount: 2,
			wantTotal: 4,
		},
		{
			name:      "limit 非法返回错误",
			log:       chatLog,
			offset:    0,
			limit:     0,
			wantCount: 0,
			wantTotal: 0,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, total, err := SliceLogMessages(tt.log(), tt.offset, tt.limit)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != tt.wantCount {
				t.Fatalf("count = %d, want %d", len(got), tt.wantCount)
			}
			if total != tt.wantTotal {
				t.Fatalf("total = %d, want %d", total, tt.wantTotal)
			}
			for i, sub := range tt.wantBodies {
				var m map[string]any
				if err := json.Unmarshal(got[i], &m); err != nil {
					t.Fatalf("message %d not valid JSON: %v", i, err)
				}
				b, _ := json.Marshal(m)
				if !contains(string(b), sub) {
					t.Errorf("message %d body %s missing %q", i, b, sub)
				}
			}
		})
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
