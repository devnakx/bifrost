package logstore

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Postgres 列表投影输入预览上限的行为钉子：与 SQLite 侧 listinputpreview_test.go 同语义，
// 分层选择与占位逻辑由 bifrost_safe_jsonb_preview 纯函数承载，realtime.turn 行仍走
// bifrost_safe_jsonb。占位文本复用同包 expectedOversizedPreview 字面量

// TestPostgresListProjectionCapsOversizedInputMessage 超大单条输入消息在 postgres 列表投影中被占位替换且有界
func TestPostgresListProjectionCapsOversizedInputMessage(t *testing.T) {
	store, db := setupPerfTestDB(t)
	ctx := context.Background()

	// stale_state 处置：删除迁移记录后强制重跑 DDL，验证 CREATE OR REPLACE 幂等无副作用
	require.NoError(t, db.Exec("DELETE FROM migrations WHERE id = ?", "logs_add_safe_jsonb_preview_function").Error)
	require.NoError(t, migrationAddSafeJsonbPreviewFunction(ctx, db, testLogger{}))

	parent := "pg-cap-parent"
	entry := &Log{
		ID:              "pg-cap-1",
		ParentRequestID: &parent,
		Timestamp:       time.Now().UTC(),
		Object:          "chat.completion",
		Provider:        "openai",
		Model:           "gpt-4o-mini",
		Status:          "success",
		ResponsesInputHistoryParsed: []schemas.ResponsesMessage{
			responsesMessage(schemas.ResponsesInputMessageRoleUser, strings.Repeat("x", 17<<20)),
		},
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, store.Create(ctx, entry))

	result, err := store.SearchLogs(ctx, SearchFilters{ParentRequestID: parent}, PaginationOptions{Limit: 1})
	require.NoError(t, err)
	require.Len(t, result.Logs, 1)

	got := result.Logs[0].ResponsesInputHistoryParsed
	require.Len(t, got, 1, "列表行只应携带被选中的末条消息")
	require.NotNil(t, got[0].Type)
	assert.Equal(t, schemas.ResponsesMessageTypeMessage, *got[0].Type, "responses 占位必须带 type=message")
	require.NotNil(t, got[0].Role)
	assert.Equal(t, schemas.ResponsesInputMessageRoleUser, *got[0].Role, "占位必须保留原 role")
	require.NotNil(t, got[0].Content)
	assertBoundedContent(t, "pg-responses-input-preview", got[0].Content.ContentStr, expectedOversizedPreview)

	data, err := sonic.Marshal(got[0])
	require.NoError(t, err)
	assert.LessOrEqual(t, len(data), maxPreviewElementBytes, "单元素序列化长度须不超过 16384 字节")
}

// TestPostgresListProjectionMalformedJsonRowStillReturned 畸形 JSON 行不阻断整页查询且原文透传
func TestPostgresListProjectionMalformedJsonRowStillReturned(t *testing.T) {
	store, _ := setupPerfTestDB(t)
	ctx := context.Background()

	parent := "pg-malformed-parent"
	entry := &Log{
		ID:              "pg-malformed-1",
		ParentRequestID: &parent,
		Timestamp:       time.Now().UTC(),
		Object:          "chat.completion",
		Provider:        "openai",
		Model:           "gpt-4o-mini",
		Status:          "success",
		InputHistory:    "not-json{",
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, store.Create(ctx, entry))

	result, err := store.SearchLogs(ctx, SearchFilters{ParentRequestID: parent}, PaginationOptions{Limit: 10})
	require.NoError(t, err, "畸形 JSON 行不得让整页列表查询报错")
	require.Len(t, result.Logs, 1)
	assert.Equal(t, "not-json{", result.Logs[0].InputHistory, "畸形原文应原样透传")
	assert.Empty(t, result.Logs[0].InputHistoryParsed, "反序列化失败时静默置空 slice")
}

// TestPostgresListProjectionEmptyArrayRowPassthrough 空数组行按规格透传不改写
func TestPostgresListProjectionEmptyArrayRowPassthrough(t *testing.T) {
	store, _ := setupPerfTestDB(t)
	ctx := context.Background()

	parent := "pg-emptyarr-parent"
	entry := &Log{
		ID:              "pg-emptyarr-1",
		ParentRequestID: &parent,
		Timestamp:       time.Now().UTC(),
		Object:          "chat.completion",
		Provider:        "openai",
		Model:           "gpt-4o-mini",
		Status:          "success",
		InputHistory:    "[]",
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, store.Create(ctx, entry))

	result, err := store.SearchLogs(ctx, SearchFilters{ParentRequestID: parent}, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	require.Len(t, result.Logs, 1)
	assert.Equal(t, "[]", result.Logs[0].InputHistory, "空数组行应原样透传")
	assert.Empty(t, result.Logs[0].InputHistoryParsed)
}

// TestPostgresListProjectionRealtimeTurnPassthrough realtime.turn 行保持完整轮次上下文
//
// 接线后 realtime.turn 分支路由到 bifrost_safe_jsonb：单元素轮次经末条提取后
// 内容不变（仅 jsonb 规范化文本），反序列化回 Go 后全量保留
func TestPostgresListProjectionRealtimeTurnPassthrough(t *testing.T) {
	store, _ := setupPerfTestDB(t)
	ctx := context.Background()

	full := strings.Repeat("r", 1<<20)
	entry := &Log{
		ID:        "pg-realtime-1",
		Timestamp: time.Now().UTC(),
		Object:    "realtime.turn",
		Provider:  "openai",
		Model:     "gpt-realtime",
		Status:    "success",
		ResponsesInputHistoryParsed: []schemas.ResponsesMessage{
			responsesMessage(schemas.ResponsesInputMessageRoleUser, full),
		},
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, store.Create(ctx, entry))

	result, err := store.SearchLogs(ctx, SearchFilters{}, PaginationOptions{Limit: 1})
	require.NoError(t, err)
	require.Len(t, result.Logs, 1)

	got := result.Logs[0].ResponsesInputHistoryParsed
	require.Len(t, got, 1, "realtime.turn 行保持完整轮次上下文")
	require.NotNil(t, got[0].Content)
	assertBoundedContent(t, "pg-realtime-full-turn", got[0].Content.ContentStr, full)
}

// TestPostgresListProjectionPreviewFunctionDirect 直调 bifrost_safe_jsonb_preview 钉住函数体各分支，
// 与 safe_jsonb_test.go 的直调先例同构；期望值经 JSON 语义比较避免 jsonb 键序/空格脆断言
func TestPostgresListProjectionPreviewFunctionDirect(t *testing.T) {
	_, db := setupPerfTestDB(t)

	capChars := maxListInputPreviewChars
	placeholder := "[preview omitted: message too large]"
	big := strings.Repeat("u", capChars*2)

	type jsonCase struct {
		name string
		in   string
		want any // 期望 JSON 解码值（数组/对象）
	}
	cases := []jsonCase{
		{
			name: "under_cap_user_message_returns_original",
			in:   `[{"role":"user","content":"hello"}]`,
			want: []any{map[string]any{"role": "user", "content": "hello"}},
		},
		{
			name: "picks_last_user_assistant_entry",
			in:   `[{"role":"user","content":"first"},{"role":"assistant","content":"second"}]`,
			want: []any{map[string]any{"role": "assistant", "content": "second"}},
		},
		{
			name: "oversized_user_replaced_by_placeholder",
			in:   `[{"type":"message","role":"user","content":"` + big + `"}]`,
			want: []any{map[string]any{"type": "message", "role": "user", "content": placeholder}},
		},
		{
			name: "no_user_assistant_falls_back_to_absolute_last_element",
			in:   `[{"type":"function_call","name":"f"},{"type":"reasoning","summary":"z"}]`,
			want: []any{map[string]any{"type": "reasoning", "summary": "z"}},
		},
		{
			name: "oversized_fallback_placeholder_has_null_role",
			in:   `[{"type":"reasoning","summary":"` + big + `"}]`,
			want: []any{map[string]any{"type": "message", "role": nil, "content": placeholder}},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got string
			err := db.Raw("SELECT bifrost_safe_jsonb_preview(?, ?)", c.in, capChars).Scan(&got).Error
			require.NoError(t, err, "bifrost_safe_jsonb_preview must not propagate parse errors")
			var gotVal, wantVal any
			require.NoError(t, sonic.Unmarshal([]byte(got), &gotVal))
			require.NoError(t, sonic.Unmarshal([]byte(jsonOf(t, c.want)), &wantVal))
			assert.Equal(t, wantVal, gotVal)
		})
	}

	t.Run("malformed_returns_raw", func(t *testing.T) {
		var got string
		require.NoError(t, db.Raw("SELECT bifrost_safe_jsonb_preview(?, ?)", "[{bad", capChars).Scan(&got).Error)
		assert.Equal(t, "[{bad", got)
	})
	t.Run("empty_array_passthrough", func(t *testing.T) {
		var got string
		require.NoError(t, db.Raw("SELECT bifrost_safe_jsonb_preview(?, ?)", "[]", capChars).Scan(&got).Error)
		assert.Equal(t, "[]", got)
	})
	t.Run("null_input_returns_null", func(t *testing.T) {
		var got *string
		require.NoError(t, db.Raw("SELECT bifrost_safe_jsonb_preview(NULL::text, ?)", capChars).Scan(&got).Error)
		assert.Nil(t, got)
	})
}

// jsonOf 把期望值编码为 JSON 文本供语义比较
func jsonOf(t *testing.T, v any) string {
	t.Helper()
	data, err := sonic.Marshal(v)
	require.NoError(t, err)
	return string(data)
}
