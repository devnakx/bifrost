package logstore

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFindByIDMessagesProjection 九列瘦投影钉子：
// 仅为 messages 端点服务，SELECT 九列 id/timestamp/object_type/has_object/content_hidden/input_history/responses_input_history/output_message/responses_output
// 前五列是 hybrid 水合必要元数据，后四列是消息实体；避免读 raw/模态列
// AfterFind 按列非空才 Unmarshal，投影后其余列零值不触发解析
func TestFindByIDMessagesProjection(t *testing.T) {
	t.Run("chat类行解析且不读raw", func(t *testing.T) {
		store := newProjectionTestStore(t)
		ctx := context.Background()

		inputMsg := schemas.ChatMessage{Role: "user", Content: &schemas.ChatMessageContent{ContentStr: stringPtr("hello chat")}}
		outputMsg := schemas.ChatMessage{Role: "assistant", Content: &schemas.ChatMessageContent{ContentStr: stringPtr("world reply")}}
		entry := &Log{
			ID:                  "msg-proj-chat-1",
			Timestamp:           time.Now().UTC(),
			Object:              "chat.completion",
			Provider:            "openai",
			Model:               "gpt-4o-mini",
			Status:              "success",
			InputHistoryParsed:  []schemas.ChatMessage{inputMsg},
			OutputMessageParsed: &outputMsg,
			RawRequest:          `{"raw":"request body should not be selected"}`,
			RawResponse:         `{"raw":"response body should not be selected"}`,
		}
		require.NoError(t, store.Create(ctx, entry))

		got, err := store.FindByIDMessagesProjection(ctx, "msg-proj-chat-1")
		require.NoError(t, err)
		require.NotNil(t, got)

		// 消息实体正确回填
		require.Len(t, got.InputHistoryParsed, 1)
		assert.Equal(t, "hello chat", *got.InputHistoryParsed[0].Content.ContentStr)
		require.NotNil(t, got.OutputMessageParsed)
		assert.Equal(t, "world reply", *got.OutputMessageParsed.Content.ContentStr)

		// 九列之外未读：raw 列保持零值，证明未 select
		assert.Empty(t, got.RawRequest, "九列投影不得读出 raw_request")
		assert.Empty(t, got.RawResponse, "九列投影不得读出 raw_response")

		// 水合元数据正确
		assert.Equal(t, "chat.completion", got.Object)
		assert.Equal(t, "msg-proj-chat-1", got.ID)
	})

	t.Run("responses类行解析", func(t *testing.T) {
		store := newProjectionTestStore(t)
		ctx := context.Background()

		body := "responses question"
		answer := "responses answer"
		msgType := schemas.ResponsesMessageTypeMessage
		entry := &Log{
			ID:        "msg-proj-resp-1",
			Timestamp: time.Now().UTC(),
			Object:    "response",
			Provider:  "openai",
			Model:     "gpt-4o-mini",
			Status:    "success",
			ResponsesInputHistoryParsed: []schemas.ResponsesMessage{
				responsesMessage(schemas.ResponsesInputMessageRoleUser, body),
			},
			ResponsesOutputParsed: []schemas.ResponsesMessage{
				{Type: &msgType, Content: &schemas.ResponsesMessageContent{ContentStr: &answer}},
			},
		}
		require.NoError(t, store.Create(ctx, entry))

		got, err := store.FindByIDMessagesProjection(ctx, "msg-proj-resp-1")
		require.NoError(t, err)
		require.NotNil(t, got)

		require.Len(t, got.ResponsesInputHistoryParsed, 1)
		require.NotNil(t, got.ResponsesInputHistoryParsed[0].Content)
		assert.Equal(t, body, *got.ResponsesInputHistoryParsed[0].Content.ContentStr)

		require.Len(t, got.ResponsesOutputParsed, 1)
		require.NotNil(t, got.ResponsesOutputParsed[0].Content)
		assert.Equal(t, answer, *got.ResponsesOutputParsed[0].Content.ContentStr)

		assert.Equal(t, "response", got.Object)
	})

	t.Run("has_object水合元数据不丢", func(t *testing.T) {
		store := newProjectionTestStore(t)
		ctx := context.Background()

		entry := &Log{
			ID:                  "msg-proj-hasobj-1",
			Timestamp:           time.Now().UTC(),
			Object:              "chat.completion",
			Provider:            "openai",
			Model:               "gpt-4o-mini",
			Status:              "success",
			HasObject:           true,
			ContentHidden:       false,
			InputHistoryParsed:  []schemas.ChatMessage{{Role: "user", Content: &schemas.ChatMessageContent{ContentStr: stringPtr("hi")}}},
			OutputMessageParsed: &schemas.ChatMessage{Role: "assistant", Content: &schemas.ChatMessageContent{ContentStr: stringPtr("ok")}},
		}
		require.NoError(t, store.Create(ctx, entry))

		got, err := store.FindByIDMessagesProjection(ctx, "msg-proj-hasobj-1")
		require.NoError(t, err)
		assert.True(t, got.HasObject, "HasObject 必须忠实读出 true，否则 hybrid 水合静默跳过导致空消息")
		assert.False(t, got.ContentHidden)
		assert.False(t, got.Timestamp.IsZero(), "timestamp 为水合 key 的一部分，不得为空")
	})

	t.Run("不存在返回ErrNotFound", func(t *testing.T) {
		store := newProjectionTestStore(t)
		ctx := context.Background()

		_, err := store.FindByIDMessagesProjection(ctx, "not-exist-id-xyz")
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrNotFound)
	})
}

func stringPtr(s string) *string { return &s }
