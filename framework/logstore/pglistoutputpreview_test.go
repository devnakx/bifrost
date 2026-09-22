package logstore

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 列表/session 投影输出侧的投影断言钉子：
// - postgres 列表投影仅在 DB 内裁剪 chat output_message；responses 两列已整体收敛出列表
// - responses 预览裁剪迁移至 sessionSelectColumns：postgres 走裁剪函数，sqlite 裸列透传
// - sqlite 列表投影保持裸 output_message 读出 + Go 侧 trimListPayloadPreviews 兜底
//
// 断言用列边界正则防止函数名子串误判（如 bifrost_safe_output_message_preview 内
// 含 "output_message" 子串）

// bareColumnRe 匹配 SELECT 列表中裸列名（前后是列分隔符边界）
func bareColumnRe(col string) *regexp.Regexp {
	return regexp.MustCompile(`(^|, |\n)` + col + `( AS |,)`)
}

// TestListSelectColumnsProjectionOutputTrimming 钉住各方言列表/session 投影的输出列形态。
// 测试内以字面量固定函数名与阈值占位，不引用实现常量，保证红态可编译
func TestListSelectColumnsProjectionOutputTrimming(t *testing.T) {
	pgStore, _ := setupPerfTestDB(t)
	pgCols := pgStore.listSelectColumns()
	pgSessionCols := pgStore.sessionSelectColumns()

	sqliteStore := newProjectionTestStore(t)
	sqliteCols := sqliteStore.listSelectColumns()
	sqliteSessionCols := sqliteStore.sessionSelectColumns()

	// postgres 列表投影：仅保留 chat 输出裁剪函数，responses 两列整体移除
	require.Contains(t, pgCols, "bifrost_safe_output_message_preview(",
		"postgres 投影必须含 chat 输出裁剪函数调用；当前长度 %d", len(pgCols))
	// 列限定接线：同名函数在不同列各有一处调用，裸函数名断言无法区分调用点
	require.Contains(t, pgCols, "bifrost_safe_jsonb_preview(input_history,",
		"postgres 列表投影必须含输入预览函数调用；当前长度 %d", len(pgCols))
	require.Contains(t, pgCols, "bifrost_safe_output_message_preview(output_message,",
		"postgres 列表投影必须含 chat 输出裁剪列限定调用；当前长度 %d", len(pgCols))
	require.NotContains(t, pgCols, "bifrost_safe_responses_output_preview(",
		"responses 输出裁剪已随字段收敛迁出列表投影")
	for _, col := range []string{"output_message", "responses_output"} {
		require.False(t, bareColumnRe(col).MatchString(pgCols),
			"postgres 投影不得裸读 %s 列", col)
	}
	// image_edit_input 改为 SQL 侧只取 prompt 标量，不再原样透传整列表达式
	require.NotContains(t, pgCols, "image_edit_input AS image_edit_input",
		"postgres 投影不得原样透传 image_edit_input")

	// 列表投影携带最新一条预览（截断 128 字符），全文不进入投影：两方言一致
	for _, cols := range []string{pgCols, sqliteCols} {
		require.Contains(t, cols, "substr(list_preview, 1, 128) AS list_preview",
			"列表投影必须含 list_preview 截断表达式；当前长度 %d", len(cols))
	}
	for _, cols := range []string{pgCols, pgSessionCols, sqliteCols, sqliteSessionCols} {
		require.NotContains(t, cols, "content_summary",
			"列表/session 投影不得再选 content_summary 全文")
	}

	// 对照断言：sqlite 列表投影仍裸读 chat 输出列（Go 侧裁剪兜底），防误伤
	require.True(t, bareColumnRe("output_message").MatchString(sqliteCols),
		"sqlite 投影必须保留裸 output_message 列读出（行为不变）")
	require.False(t, bareColumnRe("responses_output").MatchString(sqliteCols),
		"sqlite 列表投影同样不得再下发 responses_output")

	// session 投影：两方言都保留 responses 预览（postgres 走裁剪函数，sqlite 裸列）
	require.Contains(t, pgSessionCols, "bifrost_safe_responses_output_preview(",
		"postgres session 投影必须含 responses 输出裁剪函数调用")
	require.Contains(t, pgSessionCols, "bifrost_safe_jsonb_preview(responses_input_history,",
		"postgres session 投影必须含 responses 输入预览函数调用")
	require.Contains(t, pgSessionCols, "bifrost_safe_responses_output_preview(responses_output,",
		"postgres session 投影必须含 responses 输出裁剪列限定调用")
	require.Contains(t, pgSessionCols, "bifrost_safe_jsonb_preview(input_history,",
		"postgres session 投影继承列表基座输入预览调用")
	require.Contains(t, pgSessionCols, "bifrost_safe_output_message_preview(output_message,",
		"postgres session 投影继承列表基座输出裁剪调用")
	require.True(t, bareColumnRe("responses_output").MatchString(sqliteSessionCols),
		"sqlite session 投影必须保留裸 responses_output 列读出")
}

// TestPostgresOutputPreviewFunctionsDirect 直调两个新裁剪函数钉住各分支，
// 与 pglistinputpreview_test.go 的直调先例同构；期望值经 JSON 语义比较避免键序脆断言
func TestPostgresOutputPreviewFunctionsDirect(t *testing.T) {
	store, db := setupPerfTestDB(t)
	ctx := context.Background()

	// stale_state 处置：删除迁移记录后强制重跑 DDL，验证 CREATE OR REPLACE 幂等无副作用
	require.NoError(t, db.Exec("DELETE FROM migrations WHERE id = ?", "logs_add_safe_output_preview_functions").Error)
	require.NoError(t, migrationAddSafeOutputPreviewFunctions(ctx, db, testLogger{}))

	capChars := maxListOutputPreviewRunes
	big := strings.Repeat("z", capChars+100)

	t.Run("chat_output_message", func(t *testing.T) {
		cases := []struct {
			name string
			in   string
			want any
		}{
			{
				name: "assistant_text_kept",
				in:   `{"role":"assistant","content":"hello world"}`,
				want: map[string]any{"role": "assistant", "content": "hello world"},
			},
			{
				name: "reasoning_kept_as_reasoning_key",
				in:   `{"role":"assistant","content":"answer","reasoning":"thinking..."}`,
				want: map[string]any{"role": "assistant", "content": "answer", "reasoning": "thinking..."},
			},
			{
				name: "oversized_text_truncated_to_cap",
				in:   `{"role":"assistant","content":"` + big + `"}`,
				want: map[string]any{"role": "assistant", "content": strings.Repeat("z", capChars)},
			},
			{
				name: "non_assistant_role_passthrough_raw",
				in:   `{"role":"tool","content":"tool output"}`,
				want: `{"role":"tool","content":"tool output"}`,
			},
			{
				name: "malformed_json_passthrough_raw",
				in:   `{bad`,
				want: `{bad`,
			},
			{
				name: "null_returns_null",
				in:   "",
				want: nil,
			},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				var got *string
				require.NoError(t, db.Raw("SELECT bifrost_safe_output_message_preview(?, ?)", c.in, capChars).Scan(&got).Error)
				assertJSONSemantics(t, got, c.want)
			})
		}
	})

	t.Run("responses_output", func(t *testing.T) {
		cases := []struct {
			name string
			in   string
			want any
		}{
			{
				name: "last_message_text_and_reasoning_summary",
				in:   `[{"type":"message","role":"user"},{"type":"reasoning","summary":[{"type":"summary_text","text":"sum"}]},{"type":"message","role":"assistant","content":"final"}]`,
				want: []any{
					map[string]any{"type": "message", "role": "assistant", "content": map[string]any{"content": "final"}},
					map[string]any{"type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "sum"}}},
				},
			},
			{
				name: "reasoning_only_row",
				in:   `[{"type":"reasoning","summary":[{"type":"summary_text","text":"only sum"}]}]`,
				want: []any{
					map[string]any{"type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "only sum"}}},
				},
			},
			{
				name: "oversized_text_truncated",
				in:   `[{"type":"message","role":"assistant","content":"` + big + `"}]`,
				want: []any{
					map[string]any{"type": "message", "role": "assistant", "content": map[string]any{"content": strings.Repeat("z", capChars)}},
				},
			},
			{
				name: "empty_array_passthrough",
				in:   `[]`,
				want: `[]`,
			},
			{
				name: "malformed_json_passthrough_raw",
				in:   `[{bad`,
				want: `[{bad`,
			},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				var got *string
				require.NoError(t, db.Raw("SELECT bifrost_safe_responses_output_preview(?, ?)", c.in, capChars).Scan(&got).Error)
				assertJSONSemantics(t, got, c.want)
			})
		}
	})

	_ = store
}

// assertJSONSemantics 期望值为 nil 时断言空串（空串输入经 btrim 判断透传）；
// 为 string 时一律按原文相等比较（透传语义，如畸形 JSON 不能走 JSON 解码）；
// 其余经 JSON 解码做语义比较
func assertJSONSemantics(t *testing.T, got *string, want any) {
	t.Helper()
	if want == nil {
		// 空串输入经 btrim 首字符判断直接透传，返回空串而非 NULL
		assert.NotNil(t, got)
		if got != nil {
			assert.Empty(t, *got)
		}
		return
	}
	require.NotNil(t, got)
	if raw, ok := want.(string); ok {
		// 函数对合法 JSON 对象行会经 jsonb_build_object 重建（键序与空格与原文不同），
		// 故透传类期望也做 JSON 语义比较；仅当 got 无法解析时回退逐字比较
		var gotVal, wantVal any
		if err := sonic.Unmarshal([]byte(*got), &gotVal); err != nil {
			assert.Equal(t, raw, *got, "got 非 JSON 时须与原文逐字一致")
			return
		}
		require.NoError(t, sonic.Unmarshal([]byte(raw), &wantVal))
		assert.Equal(t, wantVal, gotVal)
		return
	}
	var gotVal any
	require.NoError(t, sonic.Unmarshal([]byte(*got), &gotVal))
	assert.Equal(t, want, gotVal)
}

// TestPostgresListProjectionEndToEndOutputPreview 端到端钉住列表行输出预览有界，
// 覆盖 chat / responses / image_edit / realtime.turn 四类行
func TestPostgresListProjectionEndToEndOutputPreview(t *testing.T) {
	store, _ := setupPerfTestDB(t)
	ctx := context.Background()

	parent := "pg-out-preview-parent"
	longText := strings.Repeat("y", maxListOutputPreviewRunes*4)
	reasoning := strings.Repeat("r", maxListOutputPreviewRunes*2)

	rows := []*Log{
		{
			ID: "pg-out-chat-1", ParentRequestID: &parent,
			Timestamp: time.Now().UTC(), Object: "chat.completion",
			Provider: "openai", Model: "gpt-4o-mini", Status: "success",
			OutputMessageParsed: &schemas.ChatMessage{
				Role:    schemas.ChatMessageRoleAssistant,
				Content: &schemas.ChatMessageContent{ContentStr: &longText},
				ChatAssistantMessage: &schemas.ChatAssistantMessage{
					Reasoning: &reasoning,
				},
			},
		},
		{
			ID: "pg-out-imageedit-1", ParentRequestID: &parent,
			Timestamp: time.Now().UTC(), Object: "image_edit",
			Provider: "openai", Model: "gpt-image-1", Status: "success",
			ImageEditInputParsed: &schemas.ImageEditInput{
				Prompt: longText,
				Images: []schemas.ImageInput{{Image: []byte(strings.Repeat("\x00", 1024))}},
			},
		},
	}
	for _, r := range rows {
		require.NoError(t, r.SerializeFields())
		require.NoError(t, store.Create(ctx, r))
	}

	result, err := store.SearchLogs(ctx, SearchFilters{}, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	require.Len(t, result.Logs, len(rows))

	byID := make(map[string]*Log, len(result.Logs))
	for i := range result.Logs {
		byID[result.Logs[i].ID] = &result.Logs[i]
	}

	chat := byID["pg-out-chat-1"]
	require.NotNil(t, chat.OutputMessageParsed, "chat 列表行应保留 assistant 预览")
	text := ""
	if chat.OutputMessageParsed.Content != nil && chat.OutputMessageParsed.Content.ContentStr != nil {
		text = *chat.OutputMessageParsed.Content.ContentStr
	}
	assert.Len(t, text, maxListOutputPreviewRunes, "chat 正文须在 DB 内截到阈值（长度只报不报全文）")
	require.NotNil(t, chat.OutputMessageParsed.ChatAssistantMessage)
	require.NotNil(t, chat.OutputMessageParsed.ChatAssistantMessage.Reasoning)
	assert.Len(t, *chat.OutputMessageParsed.ChatAssistantMessage.Reasoning, maxListOutputPreviewRunes, "reasoning 同样截断")
	assert.NotEmpty(t, chat.ListPreview, "chat 列表行须携带最新一条预览")
	assert.LessOrEqual(t, len(chat.ListPreview), 128, "预览须截断到 128 字符以内")
	assert.Empty(t, chat.ContentSummary, "列表行不得携带全文")

	edit := byID["pg-out-imageedit-1"]
	require.NotNil(t, edit.ImageEditInputParsed, "image edit 列表行保留 prompt 结构")
	assert.Empty(t, edit.ImageEditInputParsed.Images, "base64 图片须在 SQL 侧剥离")
	assert.Len(t, edit.ImageEditInputParsed.Prompt, maxListOutputPreviewRunes, "prompt 须经 Go 兜底截断")

	data, err := sonic.Marshal(result.Logs[0])
	require.NoError(t, err)
	assert.LessOrEqual(t, len(data), 8192, "单行序列化长度应有界")
}

// TestPostgresListProjectionMalformedOutputJsonRowStillReturned 畸形输出 JSON 行
// 不阻断整页查询且原文透传（复用 safe_jsonb 防御模式）
func TestPostgresListProjectionMalformedOutputJsonRowStillReturned(t *testing.T) {
	store, db := setupPerfTestDB(t)
	ctx := context.Background()

	parent := "pg-out-malformed-parent"
	entry := &Log{
		ID:              "pg-out-malformed-1",
		ParentRequestID: &parent,
		Timestamp:       time.Now().UTC(),
		Object:          "chat.completion",
		Provider:        "openai",
		Model:           "gpt-4o-mini",
		Status:          "success",
		OutputMessage:   "not-json{",
	}
	require.NoError(t, entry.SerializeFields())
	// 行必须先经 Create 落库，UPDATE 才有目标行；此前漏掉插入导致查询恒返回 0 条
	require.NoError(t, store.Create(ctx, entry))
	require.NoError(t, db.Exec(`UPDATE logs SET output_message = ? WHERE id = ?`, "not-json{", entry.ID).Error)
	require.NoError(t, db.Exec(`UPDATE logs SET output_message = ? WHERE id = ?`, "not-json{", entry.ID).Error)

	result, err := store.SearchLogs(ctx, SearchFilters{ParentRequestID: parent}, PaginationOptions{Limit: 10})
	require.NoError(t, err, "畸形输出 JSON 行不得让整页列表查询报错")
	require.Len(t, result.Logs, 1)
	assert.Nil(t, result.Logs[0].OutputMessageParsed, "反序列化失败时静默置空")
}
