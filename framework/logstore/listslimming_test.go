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

// 列表响应字段收敛（T2）钉子：
// - SearchLogs 列表路径返回的 Log 序列化 JSON 不含 responses_input_history /
//   responses_output 键；content_summary 截断到 512 字节
// - GetSessionLogs 投影隔离：仍返回消息预览字段，与改动前快照语义一致
// - content_search 过滤基于存储列 content_summary，不受投影收敛影响
//
// 阈值与占位以字面量固定，不引用实现常量，保证红态可编译

// task2SessionEntry 构造一行同时带 responses 输入/输出/超长 content_summary 的日志，
// 供列表收敛与 session 隔离两类测试共用
func task2SessionEntry(parent, id string, summaryLen int) *Log {
	finalAnswer := "final answer text"
	msgType := schemas.ResponsesMessageTypeMessage
	userBody := "user question"
	entry := &Log{
		ID:              id,
		ParentRequestID: &parent,
		Timestamp:       time.Now().UTC(),
		Object:          "response",
		Provider:        "openai",
		Model:           "gpt-4o-mini",
		Status:          "success",
		ContentSummary:  strings.Repeat("s", summaryLen),
		ResponsesInputHistoryParsed: []schemas.ResponsesMessage{
			responsesMessage(schemas.ResponsesInputMessageRoleUser, userBody),
		},
		ResponsesOutputParsed: []schemas.ResponsesMessage{
			{
				Type:    &msgType,
				Content: &schemas.ResponsesMessageContent{ContentStr: &finalAnswer},
			},
		},
	}
	return entry
}

// TestSearchLogsListFieldsSlimmed 列表路径返回的 Log 序列化 JSON 不含两个重字段键，
// content_summary 截到 512 字节以内（sqlite 与 postgres 双方言）
func TestSearchLogsListFieldsSlimmed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		store func(*testing.T) *RDBLogStore
	}{
		{"sqlite", newProjectionTestStore},
		{"postgres", func(t *testing.T) *RDBLogStore { s, _ := setupPerfTestDB(t); return s }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := tc.store(t)
			ctx := context.Background()

			parent := "t2-list-slim-parent"
			require.NoError(t, store.Create(ctx, task2SessionEntry(parent, "t2-list-slim-1", 4000)))

			result, err := store.SearchLogs(ctx, SearchFilters{ParentRequestID: parent}, PaginationOptions{Limit: 10})
			require.NoError(t, err)
			require.Len(t, result.Logs, 1)

			data, err := sonic.Marshal(result.Logs[0])
			require.NoError(t, err)
			var m map[string]any
			require.NoError(t, sonic.Unmarshal(data, &m))

			// 列表投影收敛后这两个重字段不再下发
			_, hasInput := m["responses_input_history"]
			_, hasOutput := m["responses_output"]
			assert.False(t, hasInput, "列表路径不得下发 responses_input_history")
			assert.False(t, hasOutput, "列表路径不得下发 responses_output")

			// content_summary 有界：512 字节 + omitempty 空串为 0 字节
			if summary, ok := m["content_summary"].(string); ok {
				assert.LessOrEqual(t, len(summary), 512,
					"content_summary 须截到 512 字节以内，实际长度 %d", len(summary))
			}
		})
	}
}

// TestSessionSelectColumnsProjectionIsolated session 投影隔离钉子：
// GetSessionLogs 仍返回 responses_input_history / responses_output 消息预览
// （内容为输入预览与输出正文），与改动前快照语义一致
func TestSessionSelectColumnsProjectionIsolated(t *testing.T) {
	store := newProjectionTestStore(t)
	ctx := context.Background()

	parent := "t2-sess-iso-parent"
	require.NoError(t, store.Create(ctx, task2SessionEntry(parent, "t2-sess-iso-1", 100)))

	session, err := store.GetSessionLogs(ctx, parent, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	require.Len(t, session.Logs, 1)

	got := session.Logs[0]
	require.NotEmpty(t, got.ResponsesInputHistoryParsed, "session 仍须返回 responses 输入预览")
	require.NotEmpty(t, got.ResponsesOutputParsed, "session 仍须返回 responses 输出预览")

	require.Len(t, got.ResponsesInputHistoryParsed, 1)
	require.NotNil(t, got.ResponsesInputHistoryParsed[0].Content)
	assert.Equal(t, "user question", *got.ResponsesInputHistoryParsed[0].Content.ContentStr)

	require.Len(t, got.ResponsesOutputParsed, 1)
	require.NotNil(t, got.ResponsesOutputParsed[0].Content)
	assert.Equal(t, "final answer text", *got.ResponsesOutputParsed[0].Content.ContentStr)
}

// TestSessionSelectColumnsKeepsPreviewExpressions 投影字符串层面钉子：
// sessionSelectColumns 保留 responses 两列（含 AS 别名形态）
func TestSessionSelectColumnsKeepsPreviewExpressions(t *testing.T) {
	sqliteCols := newProjectionTestStore(t).sessionSelectColumns()
	for _, col := range []string{"responses_input_history", "responses_output"} {
		assert.True(t, strings.Contains(sqliteCols, "AS "+col),
			"session 投影必须保留 %s 列（含 AS 别名形态）", col)
	}
}

// TestListSelectColumnsDropsResponsesPayloads list 投影字符串层面钉子：
// 收敛后 listSelectColumns 不再包含 responses_input_history / responses_output 投影
func TestListSelectColumnsDropsResponsesPayloads(t *testing.T) {
	sqliteCols := newProjectionTestStore(t).listSelectColumns()
	for _, col := range []string{"responses_input_history", "responses_output"} {
		assert.False(t, strings.Contains(sqliteCols, "AS "+col),
			"list 投影不得再选 %s 列", col)
		assert.False(t, bareColumnRe(col).MatchString(sqliteCols),
			"list 投影不得裸读 %s 列", col)
	}
}

// TestSearchLogsContentSearchStillFilters content_search 过滤：
// 过滤基于存储列 content_summary（非投影别名），投影收敛后仍须命中
func TestSearchLogsContentSearchStillFilters(t *testing.T) {
	store := newProjectionTestStore(t)
	ctx := context.Background()

	parent := "t2-fts-parent"
	matched := task2SessionEntry(parent, "t2-fts-1", 100)
	matched.ContentSummary = "the quick brown fox jumps"
	require.NoError(t, store.Create(ctx, matched))

	other := task2SessionEntry(parent, "t2-fts-2", 100)
	other.ContentSummary = "something else entirely"
	require.NoError(t, store.Create(ctx, other))

	// 命中过滤词
	hit, err := store.SearchLogs(ctx, SearchFilters{ContentSearch: "quick brown"}, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	require.Len(t, hit.Logs, 1, "content_search 命中行须返回")
	assert.Equal(t, "t2-fts-1", hit.Logs[0].ID)

	// 未命中过滤词
	miss, err := store.SearchLogs(ctx, SearchFilters{ContentSearch: "nonexistent-token"}, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	assert.Empty(t, miss.Logs, "content_search 未命中须返回空集")
}
