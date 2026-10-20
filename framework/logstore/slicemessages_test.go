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

// 生产行的 object_type 是 schemas.RequestType 的下划线形态（chat_completion 等），
// 解析后的 passthrough 行也会被改写成该类型；消息分页与角色过滤对两者都必须生效。
func TestSliceLogMessagesProductionObjectTypes(t *testing.T) {
	tests := []struct {
		name           string
		object         string
		passthrough    bool
		log            func() *Log
		wantTotal      int
		wantUserBodies []string
	}{
		{name: "chat_completion", object: "chat_completion", log: chatLog, wantTotal: 4, wantUserBodies: []string{"user-0", "user-2"}},
		{name: "chat_completion_stream", object: "chat_completion_stream", log: chatLog, wantTotal: 4, wantUserBodies: []string{"user-0", "user-2"}},
		{name: "responses_stream", object: "responses_stream", log: responsesLog, wantTotal: 4, wantUserBodies: []string{"rin-0", "rin-1"}},
		{
			name:           "parsed passthrough stored as chat_completion",
			object:         "chat_completion",
			passthrough:    true,
			log:            chatLog,
			wantTotal:      4,
			wantUserBodies: []string{"user-0", "user-2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := tt.log()
			l.Object = tt.object
			l.IsPassthrough = tt.passthrough

			full, total, err := SliceLogMessages(l, 0, 100)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if total != tt.wantTotal || len(full) != tt.wantTotal {
				t.Fatalf("full = (%d, %d), want total %d", len(full), total, tt.wantTotal)
			}

			filtered, filteredTotal, err := SliceLogMessagesWithRoles(l, 0, 100, []string{"user"})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if filteredTotal != len(tt.wantUserBodies) || len(filtered) != len(tt.wantUserBodies) {
				t.Fatalf("filtered = (%d, %d), want total %d", len(filtered), filteredTotal, len(tt.wantUserBodies))
			}
			for i, sub := range tt.wantUserBodies {
				if !contains(string(filtered[i]), sub) {
					t.Errorf("message %d body %s missing %q", i, filtered[i], sub)
				}
			}
		})
	}
}

func TestSliceLogMessagesLatestFirst(t *testing.T) {
	got, total, err := SliceLogMessagesLatestFirst(chatLog(), 0, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 4 {
		t.Fatalf("total = %d, want 4", total)
	}
	for i, sub := range []string{"output-3", "user-2"} {
		if !contains(string(got[i]), sub) {
			t.Errorf("message %d body %s missing %q", i, got[i], sub)
		}
	}
}

func rolesResponsesLog() *Log {
	msgType := schemas.ResponsesMessageTypeMessage
	reasoningType := schemas.ResponsesMessageTypeReasoning
	fnCallType := schemas.ResponsesMessageTypeFunctionCall
	fnOutType := schemas.ResponsesMessageTypeFunctionCallOutput
	searchOutType := schemas.ResponsesMessageTypeToolSearchOutput
	roleUser := schemas.ResponsesInputMessageRoleUser
	roleAssistant := schemas.ResponsesInputMessageRoleAssistant
	roleDeveloper := schemas.ResponsesInputMessageRoleDeveloper
	return &Log{
		Object: "responses",
		ResponsesInputHistoryParsed: []schemas.ResponsesMessage{
			{Type: &msgType, Role: &roleUser, Content: &schemas.ResponsesMessageContent{ContentStr: ptrStr("role-user-0")}},
			{Type: &msgType, Role: &roleDeveloper, Content: &schemas.ResponsesMessageContent{ContentStr: ptrStr("role-developer-1")}},
		},
		ResponsesOutputParsed: []schemas.ResponsesMessage{
			{Type: &reasoningType, Role: &roleAssistant},
			{Type: &fnCallType, Role: &roleAssistant},
			{Type: &fnOutType, Role: &roleAssistant},
			{Type: &searchOutType, Role: &roleAssistant},
			{Type: &msgType, Role: &roleAssistant, Content: &schemas.ResponsesMessageContent{ContentStr: ptrStr("role-assistant-2")}},
			{Content: &schemas.ResponsesMessageContent{ContentStr: ptrStr("role-missing-3")}},
		},
	}
}

func TestSliceLogMessagesWithRoles(t *testing.T) {
	tests := []struct {
		name      string
		log       func() *Log
		roles     []string
		offset    int
		limit     int
		wantCount int
		wantTotal int
	}{
		{name: "chat 只取 user", log: chatLog, roles: []string{"user"}, offset: 0, limit: 10, wantCount: 2, wantTotal: 2},
		{name: "chat 默认 user+assistant 全量", log: chatLog, roles: []string{"user", "assistant"}, offset: 0, limit: 10, wantCount: 4, wantTotal: 4},
		{name: "responses tool 命中 _call 与 _call_output 及 tool_search_output", log: rolesResponsesLog, roles: []string{"tool"}, offset: 0, limit: 10, wantCount: 3, wantTotal: 3},
		{name: "responses reasoning 单独成类", log: rolesResponsesLog, roles: []string{"reasoning"}, offset: 0, limit: 10, wantCount: 1, wantTotal: 1},
		{name: "responses developer 归入 system", log: rolesResponsesLog, roles: []string{"system"}, offset: 0, limit: 10, wantCount: 1, wantTotal: 1},
		{name: "responses 缺失 role 与 type 归入 assistant", log: rolesResponsesLog, roles: []string{"assistant"}, offset: 0, limit: 10, wantCount: 2, wantTotal: 2},
		{name: "responses user 过滤后分页", log: rolesResponsesLog, roles: []string{"user"}, offset: 0, limit: 10, wantCount: 1, wantTotal: 1},
		{name: "过滤后 offset 越界返回空切片加过滤总数", log: chatLog, roles: []string{"user"}, offset: 100, limit: 10, wantCount: 0, wantTotal: 2},
		{name: "过滤后分页第二页", log: chatLog, roles: []string{"user"}, offset: 1, limit: 1, wantCount: 1, wantTotal: 2},
		{name: "空 roles 等价全量", log: chatLog, roles: []string{}, offset: 0, limit: 10, wantCount: 4, wantTotal: 4},
		{name: "nil roles 等价全量", log: chatLog, roles: nil, offset: 0, limit: 10, wantCount: 4, wantTotal: 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, total, err := SliceLogMessagesWithRoles(tt.log(), tt.offset, tt.limit, tt.roles)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != tt.wantCount {
				t.Fatalf("count = %d, want %d", len(got), tt.wantCount)
			}
			if total != tt.wantTotal {
				t.Fatalf("total = %d, want %d", total, tt.wantTotal)
			}
		})
	}
}

func TestSliceLogMessagesWithRolesChatBodies(t *testing.T) {
	got, total, err := SliceLogMessagesWithRoles(chatLog(), 0, 10, []string{"user"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 2 {
		t.Fatalf("total = %d, want 2", total)
	}
	for i, sub := range []string{"user-0", "user-2"} {
		if !contains(string(got[i]), sub) {
			t.Errorf("message %d body %s missing %q", i, got[i], sub)
		}
	}
}

func TestSliceLogMessagesWithRolesBackwardCompat(t *testing.T) {
	for _, tc := range []struct {
		name string
		log  func() *Log
	}{
		{"chat", chatLog},
		{"responses", responsesLog},
		{"roles 混合", rolesResponsesLog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			full, fullTotal, err := SliceLogMessages(tc.log(), 0, 100)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			filtered, filteredTotal, err := SliceLogMessagesWithRoles(tc.log(), 0, 100, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if filteredTotal != fullTotal {
				t.Fatalf("total = %d, want %d", filteredTotal, fullTotal)
			}
			if len(filtered) != len(full) {
				t.Fatalf("count = %d, want %d", len(filtered), len(full))
			}
			for i := range full {
				if string(filtered[i]) != string(full[i]) {
					t.Fatalf("message %d differs from unfiltered", i)
				}
			}

			fullDesc, fullDescTotal, err := SliceLogMessagesLatestFirst(tc.log(), 0, 100)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			filteredDesc, filteredDescTotal, err := SliceLogMessagesLatestFirstWithRoles(tc.log(), 0, 100, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if filteredDescTotal != fullDescTotal {
				t.Fatalf("desc total = %d, want %d", filteredDescTotal, fullDescTotal)
			}
			if len(filteredDesc) != len(fullDesc) {
				t.Fatalf("desc count = %d, want %d", len(filteredDesc), len(fullDesc))
			}
			for i := range fullDesc {
				if string(filteredDesc[i]) != string(fullDesc[i]) {
					t.Fatalf("desc message %d differs from unfiltered", i)
				}
			}
		})
	}
}

func TestSliceLogMessagesWithRolesDescComposition(t *testing.T) {
	got, total, err := SliceLogMessagesLatestFirstWithRoles(chatLog(), 0, 10, []string{"user"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 2 {
		t.Fatalf("total = %d, want 2", total)
	}
	if len(got) != 2 {
		t.Fatalf("count = %d, want 2", len(got))
	}
	for i, sub := range []string{"user-2", "user-0"} {
		if !contains(string(got[i]), sub) {
			t.Errorf("message %d body %s missing %q", i, got[i], sub)
		}
	}
}

func TestSliceLogMessagesWithRolesDeveloperChat(t *testing.T) {
	devLog := &Log{
		Object: "chat.completion",
		InputHistoryParsed: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleDeveloper, Content: &schemas.ChatMessageContent{ContentStr: ptrStr("dev-0")}},
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: ptrStr("user-1")}},
		},
	}
	got, total, err := SliceLogMessagesWithRoles(devLog, 0, 10, []string{"system"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 1 {
		t.Fatalf("total = %d, want 1", total)
	}
	if !contains(string(got[0]), "dev-0") {
		t.Errorf("body %s missing %q", got[0], "dev-0")
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
