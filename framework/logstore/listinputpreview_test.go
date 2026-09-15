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

// 列表投影输入预览上限的行为钉子：超大单条 chat 输入消息在 SQL 读取阶段被替换为
// 保留原 role 的最小合法占位消息；详情接口 FindByID 保持全量；realtime.turn 行豁免；
// 列表投影已不下发 responses_input_history/responses_output，session 投影独立保留
// 两列预览并继承同一上限
//
// 占位文本在此以字面量固定而不引用实现常量，保证红态（常量尚未落地）可编译
const expectedOversizedPreview = "[preview omitted: message too large]"

// maxPreviewElementBytes 单元素序列化长度上限（4096 字符 × 最坏 4 字节 UTF-8）
const maxPreviewElementBytes = 16384

// responsesMessage 构造指定 role 与正文的 responses 输入消息
func responsesMessage(role schemas.ResponsesMessageRoleType, body string) schemas.ResponsesMessage {
	msgType := schemas.ResponsesMessageTypeMessage
	return schemas.ResponsesMessage{
		Type:    &msgType,
		Role:    &role,
		Content: &schemas.ResponsesMessageContent{ContentStr: &body},
	}
}

// assertBoundedContent 断言内容逐字节一致但失败信息只报长度，避免把 MB 级载荷打进测试输出
func assertBoundedContent(t *testing.T, label string, got *string, want string) {
	t.Helper()
	if got == nil {
		t.Errorf("%s: content 为 nil 期望长度 %d", label, len(want))
		return
	}
	if *got != want {
		t.Errorf("%s: 内容不匹配 期望长度 %d 实际长度 %d", label, len(want), len(*got))
	}
}

// TestListProjectionDropsResponsesInputPayload 列表行不再下发 responses_input_history：
// 超大载荷不得经列表响应泄漏；responses 预览裁剪行为由 session 投影用例覆盖
func TestListProjectionDropsResponsesInputPayload(t *testing.T) {
	store := newProjectionTestStore(t)
	ctx := context.Background()

	// 约 17MB 对齐生产实测的单条巨型输入消息规模
	entry := &Log{
		ID:                          "oversized-1",
		Timestamp:                   time.Now().UTC(),
		Object:                      "chat.completion",
		Provider:                    "openai",
		Model:                       "gpt-4o-mini",
		Status:                      "success",
		ResponsesInputHistoryParsed: []schemas.ResponsesMessage{responsesMessage(schemas.ResponsesInputMessageRoleUser, strings.Repeat("x", 17<<20))},
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, store.Create(ctx, entry))

	result, err := store.SearchLogs(ctx, SearchFilters{}, PaginationOptions{Limit: 1})
	require.NoError(t, err)
	require.Len(t, result.Logs, 1)

	assert.Empty(t, result.Logs[0].ResponsesInputHistoryParsed, "列表行不得携带 responses_input_history")
	assert.Empty(t, result.Logs[0].ResponsesInputHistory, "原始列同样不得随列表读出")
}

// TestSessionProjectionPrefersSmallerEarlierUserMessage session 投影末条 user/assistant 未超限时原样返回
//
// 巨型 assistant 在前、小 user 在后：被选中的是最后一条 user/assistant 条目，小 user 原文透传。
// 小 user 长度落在 1025–4096 区间是刻意的：阈值探针把上限临时降到 1024 时，
// 该消息同样超限被占位替换，本用例必须转红以证明阈值真实生效
func TestSessionProjectionPrefersSmallerEarlierUserMessage(t *testing.T) {
	store := newProjectionTestStore(t)
	ctx := context.Background()

	small := strings.Repeat("u", 2048)
	userRole := schemas.ResponsesInputMessageRoleUser
	msgType := schemas.ResponsesMessageTypeMessage
	parent := "prefer-parent-1"
	entry := &Log{
		ID:              "prefer-1",
		ParentRequestID: &parent,
		Timestamp:       time.Now().UTC(),
		Object:          "chat.completion",
		Provider:        "openai",
		Model:           "gpt-4o-mini",
		Status:          "success",
		ResponsesInputHistoryParsed: []schemas.ResponsesMessage{
			responsesMessage(schemas.ResponsesInputMessageRoleAssistant, strings.Repeat("a", 1<<20)),
			{
				Type:    &msgType,
				Role:    &userRole,
				Content: &schemas.ResponsesMessageContent{ContentStr: &small},
			},
		},
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, store.Create(ctx, entry))

	session, err := store.GetSessionLogs(ctx, parent, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	require.Len(t, session.Logs, 1)

	got := session.Logs[0].ResponsesInputHistoryParsed
	require.Len(t, got, 1, "session 行只应携带被选中的末条 user/assistant 消息")
	require.NotNil(t, got[0].Role)
	assert.Equal(t, schemas.ResponsesInputMessageRoleUser, *got[0].Role)
	require.NotNil(t, got[0].Content)
	assertBoundedContent(t, "small-user-original", got[0].Content.ContentStr, small)
}

// TestListProjectionChatSideSameGuard chat 侧输入历史同受上限约束
func TestListProjectionChatSideSameGuard(t *testing.T) {
	store := newProjectionTestStore(t)
	ctx := context.Background()

	big := strings.Repeat("c", 17<<20)
	entry := &Log{
		ID:                 "chat-oversized-1",
		Timestamp:          time.Now().UTC(),
		Object:             "chat.completion",
		Provider:           "openai",
		Model:              "gpt-4o-mini",
		Status:             "success",
		InputHistoryParsed: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &big}}},
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, store.Create(ctx, entry))

	result, err := store.SearchLogs(ctx, SearchFilters{}, PaginationOptions{Limit: 1})
	require.NoError(t, err)
	require.Len(t, result.Logs, 1)

	got := result.Logs[0].InputHistoryParsed
	require.Len(t, got, 1)
	assert.Equal(t, schemas.ChatMessageRoleUser, got[0].Role, "占位必须保留原 role")
	require.NotNil(t, got[0].Content)
	assertBoundedContent(t, "chat-input-preview", got[0].Content.ContentStr, expectedOversizedPreview)

	data, err := sonic.Marshal(got[0])
	require.NoError(t, err)
	assert.LessOrEqual(t, len(data), maxPreviewElementBytes, "单元素序列化长度须不超过 16384 字节")
}

// TestDetailUnaffectedByListCap 详情接口 FindByID 不受列表上限影响仍返回原始全量
func TestDetailUnaffectedByListCap(t *testing.T) {
	store := newProjectionTestStore(t)
	ctx := context.Background()

	full := strings.Repeat("d", 1<<20)
	entry := &Log{
		ID:                          "detail-1",
		Timestamp:                   time.Now().UTC(),
		Object:                      "chat.completion",
		Provider:                    "openai",
		Model:                       "gpt-4o-mini",
		Status:                      "success",
		ResponsesInputHistoryParsed: []schemas.ResponsesMessage{responsesMessage(schemas.ResponsesInputMessageRoleUser, full)},
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, store.Create(ctx, entry))

	detail, err := store.FindByID(ctx, "detail-1")
	require.NoError(t, err)
	require.Len(t, detail.ResponsesInputHistoryParsed, 1)
	require.NotNil(t, detail.ResponsesInputHistoryParsed[0].Content)
	assertBoundedContent(t, "detail-full-payload", detail.ResponsesInputHistoryParsed[0].Content.ContentStr, full)
}

// TestRealtimeTurnOnlyOnSessionProjection realtime.turn 完整轮次不再走列表投影，
// 仅 session 投影保持整轮透传不裁剪
func TestRealtimeTurnOnlyOnSessionProjection(t *testing.T) {
	store := newProjectionTestStore(t)
	ctx := context.Background()

	full := strings.Repeat("r", 1<<20)
	parent := "rt-parent-1"
	entry := &Log{
		ID:                          "rt-1",
		ParentRequestID:             &parent,
		Timestamp:                   time.Now().UTC(),
		Object:                      "realtime.turn",
		Provider:                    "openai",
		Model:                       "gpt-realtime",
		Status:                      "success",
		ResponsesInputHistoryParsed: []schemas.ResponsesMessage{responsesMessage(schemas.ResponsesInputMessageRoleUser, full)},
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, store.Create(ctx, entry))

	result, err := store.SearchLogs(ctx, SearchFilters{}, PaginationOptions{Limit: 1})
	require.NoError(t, err)
	require.Len(t, result.Logs, 1)
	assert.Empty(t, result.Logs[0].ResponsesInputHistoryParsed, "列表行不得携带 responses_input_history")

	session, err := store.GetSessionLogs(ctx, parent, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	require.Len(t, session.Logs, 1)

	got := session.Logs[0].ResponsesInputHistoryParsed
	require.Len(t, got, 1, "session 行保持完整轮次上下文")
	require.NotNil(t, got[0].Content)
	assertBoundedContent(t, "realtime-full-turn", got[0].Content.ContentStr, full)
}

// TestSessionLogsInheritCap 会话视图复用列表投影故一并继承上限
func TestSessionLogsInheritCap(t *testing.T) {
	store := newProjectionTestStore(t)
	ctx := context.Background()

	parent := "sess-parent-1"
	entry := &Log{
		ID:                          "sess-child-1",
		ParentRequestID:             &parent,
		Timestamp:                   time.Now().UTC(),
		Object:                      "chat.completion",
		Provider:                    "openai",
		Model:                       "gpt-4o-mini",
		Status:                      "success",
		ResponsesInputHistoryParsed: []schemas.ResponsesMessage{responsesMessage(schemas.ResponsesInputMessageRoleUser, strings.Repeat("x", 1<<20))},
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, store.Create(ctx, entry))

	session, err := store.GetSessionLogs(ctx, parent, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	require.Len(t, session.Logs, 1)

	got := session.Logs[0].ResponsesInputHistoryParsed
	require.Len(t, got, 1)
	require.NotNil(t, got[0].Role)
	assert.Equal(t, schemas.ResponsesInputMessageRoleUser, *got[0].Role)
	require.NotNil(t, got[0].Content)
	assertBoundedContent(t, "session-input-preview", got[0].Content.ContentStr, expectedOversizedPreview)

	data, err := sonic.Marshal(got[0])
	require.NoError(t, err)
	assert.LessOrEqual(t, len(data), maxPreviewElementBytes, "单元素序列化长度须不超过 16384 字节")
}

// newContentSummarySplitFixture 构造双 transcript 夹具：chat 双输入 + 单条输出，
// responses 单输入 + 单输出，各段用特异标记区分
func newContentSummarySplitFixture() *Log {
	return &Log{
		ID:        "split-1",
		Timestamp: time.Now().UTC(),
		Object:    "chat.completion",
		Provider:  "openai",
		Model:     "gpt-4o-mini",
		Status:    "success",
		InputHistoryParsed: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: strPtr("split-first-alpha")}},
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: strPtr("split-second-beta")}},
		},
		OutputMessageParsed: &schemas.ChatMessage{
			Role:    schemas.ChatMessageRoleAssistant,
			Content: &schemas.ChatMessageContent{ContentStr: strPtr("split-answer-epsilon")},
		},
		ResponsesInputHistoryParsed: []schemas.ResponsesMessage{responsesMessage(schemas.ResponsesInputMessageRoleUser, "split-resp-gamma")},
		ResponsesOutputParsed:       []schemas.ResponsesMessage{responsesMessage(schemas.ResponsesInputMessageRoleAssistant, "split-resp-delta")},
	}
}

// TestContentSummarySplit_BuildInputReturnsLatestOnly 锁定预览语义：
// BuildInputContentSummary 仍只返回最新一条（输出 assistant 晚于输入 user）
func TestContentSummarySplit_BuildInputReturnsLatestOnly(t *testing.T) {
	entry := newContentSummarySplitFixture()
	assert.Equal(t, "split-answer-epsilon", entry.BuildInputContentSummary())
}

// TestContentSummarySplit_BuildContentSummaryHasDualTranscripts 锁定全量语义：
// BuildContentSummary 含双 transcript 全量，且与单条预览不等
func TestContentSummarySplit_BuildContentSummaryHasDualTranscripts(t *testing.T) {
	entry := newContentSummarySplitFixture()
	full := entry.BuildContentSummary()
	for _, marker := range []string{"split-first-alpha", "split-second-beta", "split-resp-gamma", "split-resp-delta", "split-answer-epsilon"} {
		assert.Contains(t, full, marker)
	}
	assert.NotEqual(t, entry.BuildInputContentSummary(), full)
}

// TestContentSummarySplit_FullTextAndPreview 双断言对照：SerializeFields 后
// content_summary 含双 transcript 全量，list_preview 仅为最新单条
func TestContentSummarySplit_FullTextAndPreview(t *testing.T) {
	entry := newContentSummarySplitFixture()
	require.NoError(t, entry.SerializeFields())
	for _, marker := range []string{"split-first-alpha", "split-second-beta", "split-resp-gamma", "split-resp-delta"} {
		assert.Contains(t, entry.ContentSummary, marker)
	}
	assert.Equal(t, "split-answer-epsilon", entry.ListPreview)
}

// TestListPreview_EmptyFallbackToFullSummary list_preview 为空回退：
// 无 input-only 命中时（仅错误详情）回退到全量摘要
func TestListPreview_EmptyFallbackToFullSummary(t *testing.T) {
	entry := &Log{
		ID:        "split-fallback-1",
		Timestamp: time.Now().UTC(),
		Object:    "chat.completion",
		Provider:  "openai",
		Model:     "gpt-4o-mini",
		Status:    "error",
		ErrorDetailsParsed: &schemas.BifrostError{
			Error: &schemas.ErrorField{Message: "split-fallback-zeta"},
		},
	}
	require.NoError(t, entry.SerializeFields())
	assert.Contains(t, entry.ContentSummary, "split-fallback-zeta")
	assert.Equal(t, "split-fallback-zeta", entry.ListPreview)
}

// TestListProjectionMalformedJsonRowStillReturned 畸形 JSON 行不阻断整页查询且该行正常返回
//
// json_valid 守卫使畸形行走 ELSE 原样透传分支；DeserializeFields 对畸形 JSON 静默置空
// slice（tables.go），因此占位符严格合法只是显示正确性依赖而非查询存活依赖
func TestListProjectionMalformedJsonRowStillReturned(t *testing.T) {
	store := newProjectionTestStore(t)
	ctx := context.Background()

	entry := &Log{
		ID:           "malformed-1",
		Timestamp:    time.Now().UTC(),
		Object:       "chat.completion",
		Provider:     "openai",
		Model:        "gpt-4o-mini",
		Status:       "success",
		InputHistory: "not-json{",
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, store.Create(ctx, entry))

	result, err := store.SearchLogs(ctx, SearchFilters{}, PaginationOptions{Limit: 10})
	require.NoError(t, err, "畸形 JSON 行不得让整页列表查询报错")
	require.Len(t, result.Logs, 1)
	assert.Equal(t, "not-json{", result.Logs[0].InputHistory, "畸形原文应原样透传")
	assert.Empty(t, result.Logs[0].InputHistoryParsed, "反序列化失败时静默置空 slice")
}
