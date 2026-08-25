package logstore

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/objectstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMessagesPayloadFields_Direction 断言 messagesPayloadFields 方向正确：仅四列消息实体，不得照抄 detailMetaPayloadFields
func TestMessagesPayloadFields_Direction(t *testing.T) {
	expected := []string{"input_history", "responses_input_history", "output_message", "responses_output"}
	assert.Equal(t, expected, messagesPayloadFields, "messagesPayloadFields 必须为四列消息实体")
	// 确保与 detailMetaPayloadFields 方向相反（detail 排除这四列中的两列）
	for _, f := range expected {
		found := false
		for _, df := range detailMetaPayloadFields {
			if f == df {
				found = true
				break
			}
		}
		// input_history / output_message 在 detail 中存在（detail 排除 responses_* + raw），而 responses_* 不在 detail
		if f == "input_history" || f == "output_message" {
			assert.True(t, found, "%s 应在 detailMeta 中", f)
		} else {
			assert.False(t, found, "%s 不应在 detailMeta 中（方向相反）", f)
		}
	}
	assert.NotEqual(t, detailMetaPayloadFields, messagesPayloadFields, "不得照抄 detailMetaPayloadFields")
	assert.Len(t, messagesPayloadFields, 4)
}

// TestHybrid_FindByIDMessagesProjection_Contract 契约：快路径+回退均以 messagesPayloadFields 的4字段水合，而非 detail 字段集
func TestHybrid_FindByIDMessagesProjection_Contract(t *testing.T) {
	ctx := context.Background()

	// 用真实 RDB 作为 inner，验证九列投影 + hydratePrune 行为
	inner, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: filepath.Join(t.TempDir(), "hybrid-msg-contract.db")}, hybridTestLogger{})
	require.NoError(t, err)
	defer inner.Close(ctx)

	objStore := objectstore.NewInMemoryObjectStore()
	hybrid := newHybridLogStore(inner, objStore, "test-contract", hybridTestLogger{}, nil)
	defer hybrid.Close(ctx)

	// 构造带全量 payload 的日志：消息列 + 非消息列（params/token_usage/raw）
	input := "contract input"
	output := "contract output"
	paramsMap := map[string]interface{}{"temperature": 0.9}
	entry := &Log{
		ID:                 "contract-msg-1",
		Timestamp:          time.Now().UTC(),
		Provider:           "openai",
		Model:              "gpt-4o-mini",
		Object:             "chat.completion",
		Status:             "success",
		InputHistoryParsed: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &input}}},
		OutputMessageParsed: &schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: &output}},
		ParamsParsed:       paramsMap,
		TokenUsageParsed:   &schemas.BifrostLLMUsage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
		RawRequest:         `{"raw":"request"}`,
		RawResponse:        `{"raw":"response"}`,
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, hybrid.Create(ctx, entry))
	waitForOffload(t, inner, entry.ID)

	// 快路径：inner 支持 FindByIDMessagesProjection
	got, err := hybrid.FindByIDMessagesProjection(ctx, entry.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	// 消息四列应被水合
	assert.NotEmpty(t, got.InputHistory, "input_history 应被水合")
	assert.NotEmpty(t, got.OutputMessage, "output_message 应被水合")
	// 非消息 payload 列应被 prune（messagesPayloadFields 未包含它们）
	assert.Empty(t, got.Params, "params 不在 messagesPayloadFields，应被 prune")
	assert.Empty(t, got.Tools, "tools 不在 messagesPayloadFields，应被 prune")
	assert.Empty(t, got.TokenUsage, "token_usage 不在 messagesPayloadFields，应被 prune")
	assert.Empty(t, got.RawRequest, "raw_request 不在 messagesPayloadFields，应被 prune")
	assert.Empty(t, got.RawResponse, "raw_response 不在 messagesPayloadFields，应被 prune")
	// 仍能分页
	msgs, total, err := SliceLogMessages(got, 0, 10)
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	assert.Len(t, msgs, 2)

	// 验证回退一致性：hybrid 回退分支（FindByID + hydrate 4字段）应与快路径一致
	fullViaHybrid, err := hybrid.FindByID(ctx, entry.ID)
	require.NoError(t, err)
	require.NotNil(t, fullViaHybrid)
	pruneUnrequestedPayloadFields(fullViaHybrid, messagesPayloadFields)
	assert.NotEmpty(t, fullViaHybrid.InputHistory, "回退 prune 后 input_history 应保留")
	assert.NotEmpty(t, fullViaHybrid.OutputMessage, "回退 prune 后 output_message 应保留")
	assert.Empty(t, fullViaHybrid.Params, "回退 prune 后 params 应被清理")
	assert.Empty(t, fullViaHybrid.TokenUsage, "回退 prune 后 token_usage 应被清理")
	assert.Empty(t, fullViaHybrid.RawRequest, "回退 prune 后 raw_request 应被清理")
	assert.Equal(t, got.InputHistory, fullViaHybrid.InputHistory)
	assert.Equal(t, got.OutputMessage, fullViaHybrid.OutputMessage)
}
