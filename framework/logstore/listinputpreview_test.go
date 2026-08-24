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

// 列表投影输入预览上限的行为钉子：超大单条输入消息在 SQL 读取阶段被替换为
// 保留原 role 的最小合法占位消息；详情接口 FindByID 保持全量；realtime.turn 行豁免；
// GetSessionLogs 复用列表投影故一并继承上限
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

// TestListProjectionCapsOversizedInputMessage 超大单条输入消息在列表投影中被占位替换且有界
func TestListProjectionCapsOversizedInputMessage(t *testing.T) {
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

	got := result.Logs[0].ResponsesInputHistoryParsed
	require.Len(t, got, 1, "列表行只应携带被选中的末条消息")
	require.NotNil(t, got[0].Type)
	assert.Equal(t, schemas.ResponsesMessageTypeMessage, *got[0].Type, "responses 占位必须带 type=message")
	require.NotNil(t, got[0].Role)
	assert.Equal(t, schemas.ResponsesInputMessageRoleUser, *got[0].Role, "占位必须保留原 role")
	require.NotNil(t, got[0].Content)
	assertBoundedContent(t, "responses-input-preview", got[0].Content.ContentStr, expectedOversizedPreview)

	data, err := sonic.Marshal(got[0])
	require.NoError(t, err)
	assert.LessOrEqual(t, len(data), maxPreviewElementBytes, "单元素序列化长度须不超过 16384 字节")
}

// TestListProjectionPrefersSmallerEarlierUserMessage 末条 user/assistant 未超限时原样返回
//
// 巨型 assistant 在前、小 user 在后：被选中的是最后一条 user/assistant 条目，小 user 原文透传。
// 小 user 长度落在 1025–4096 区间是刻意的：阈值探针把上限临时降到 1024 时，
// 该消息同样超限被占位替换，本用例必须转红以证明阈值真实生效
func TestListProjectionPrefersSmallerEarlierUserMessage(t *testing.T) {
	store := newProjectionTestStore(t)
	ctx := context.Background()

	small := strings.Repeat("u", 2048)
	userRole := schemas.ResponsesInputMessageRoleUser
	msgType := schemas.ResponsesMessageTypeMessage
	entry := &Log{
		ID:        "prefer-1",
		Timestamp: time.Now().UTC(),
		Object:    "chat.completion",
		Provider:  "openai",
		Model:     "gpt-4o-mini",
		Status:    "success",
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

	result, err := store.SearchLogs(ctx, SearchFilters{}, PaginationOptions{Limit: 1})
	require.NoError(t, err)
	require.Len(t, result.Logs, 1)

	got := result.Logs[0].ResponsesInputHistoryParsed
	require.Len(t, got, 1, "列表行只应携带被选中的末条 user/assistant 消息")
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

// TestRealtimeTurnExemptFromCap realtime.turn 行整行透传不裁剪
func TestRealtimeTurnExemptFromCap(t *testing.T) {
	store := newProjectionTestStore(t)
	ctx := context.Background()

	full := strings.Repeat("r", 1<<20)
	entry := &Log{
		ID:                          "rt-1",
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

	got := result.Logs[0].ResponsesInputHistoryParsed
	require.Len(t, got, 1, "realtime.turn 行保持完整轮次上下文")
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
