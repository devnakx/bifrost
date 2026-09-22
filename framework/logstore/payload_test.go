package logstore

import (
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractPayload_RoundTrip(t *testing.T) {
	metadata := `{"cortex-user-id":"user-123"}`
	log := &Log{
		ID:                      "test-1",
		InputHistory:            `[{"role":"user","content":"hello"}]`,
		ResponsesInputHistory:   `[{"role":"user","content":"hi"}]`,
		OutputMessage:           `{"role":"assistant","content":"world"}`,
		ResponsesOutput:         `[{"role":"assistant","content":"there"}]`,
		EmbeddingOutput:         `[{"embedding":[0.1]}]`,
		RerankOutput:            `[{"score":0.9}]`,
		Params:                  `{"temperature":0.7}`,
		Tools:                   `[{"name":"tool1"}]`,
		ToolCalls:               `[{"id":"tc1"}]`,
		SpeechInput:             `{"input":"text"}`,
		TranscriptionInput:      `{"file":"test.mp3"}`,
		ImageGenerationInput:    `{"prompt":"cat"}`,
		ImageEditInput:          `{"prompt":"edit cat"}`,
		ImageVariationInput:     `{"image":"base64img"}`,
		VideoGenerationInput:    `{"prompt":"dog"}`,
		SpeechOutput:            `{"audio":"base64"}`,
		TranscriptionOutput:     `{"text":"hello"}`,
		ImageGenerationOutput:   `{"url":"http://img"}`,
		ListModelsOutput:        `[{"id":"model1"}]`,
		VideoGenerationOutput:   `{"id":"vid1"}`,
		VideoRetrieveOutput:     `{"status":"ready"}`,
		VideoDownloadOutput:     `{"url":"http://vid"}`,
		VideoListOutput:         `{"videos":[]}`,
		VideoDeleteOutput:       `{"deleted":true}`,
		CacheDebug:              `{"hit":true}`,
		GuardrailDebug:          `{"judge_calls":[{"total_tokens":18}]}`,
		TokenUsage:              `{"total_tokens":100}`,
		ErrorDetails:            `{"error":"bad"}`,
		RawRequest:              `{"method":"POST"}`,
		RawResponse:             `{"status":200}`,
		PassthroughRequestBody:  `body-req`,
		PassthroughResponseBody: `body-resp`,
		RoutingEngineLogs:       `routing log`,
		Metadata:                &metadata,
	}

	payload := ExtractPayload(log)
	// +1 for metadata, +6 for the always-present index fields (provider, model,
	// status, timestamp, selected_key_id, selected_key_name) added in #6070.
	assert.Equal(t, len(payloadFields)+1+6, len(payload), "payload map should have all payload fields plus metadata and index fields")
	assert.Equal(t, `[{"role":"user","content":"hello"}]`, payload["input_history"])
	assert.Equal(t, `{"role":"assistant","content":"world"}`, payload["output_message"])
	assert.Equal(t, `{"judge_calls":[{"total_tokens":18}]}`, payload["guardrail_debug"])
	assert.Equal(t, `routing log`, payload["routing_engine_logs"])
	assert.Equal(t, metadata, payload["metadata"], "metadata must be written to the snapshot for object consumers")

	// Clear and verify.
	ClearPayload(log)
	assert.Empty(t, log.InputHistory)
	assert.Empty(t, log.OutputMessage)
	assert.Empty(t, log.RawRequest)
	assert.Empty(t, log.GuardrailDebug)
	assert.Empty(t, log.RoutingEngineLogs)
	require.NotNil(t, log.Metadata)
	assert.Equal(t, metadata, *log.Metadata)

	// Marshal and merge back.
	data, err := MarshalPayload(payload)
	require.NoError(t, err)

	// Metadata is DB-authoritative: the snapshot carries a backup copy, but
	// MergePayloadFromJSON must NOT override the live DB value with it. Simulate
	// a DB row whose metadata was updated after the snapshot was written, then
	// confirm the merge leaves it untouched.
	dbMetadata := `{"cortex-user-id":"user-456"}`
	log.Metadata = &dbMetadata
	log.MetadataParsed = nil
	err = MergePayloadFromJSON(log, data)
	require.NoError(t, err)
	assert.Equal(t, `[{"role":"user","content":"hello"}]`, log.InputHistory)
	assert.Equal(t, `{"role":"assistant","content":"world"}`, log.OutputMessage)
	assert.Equal(t, `{"judge_calls":[{"total_tokens":18}]}`, log.GuardrailDebug)
	assert.Equal(t, `routing log`, log.RoutingEngineLogs)
	require.NotNil(t, log.Metadata)
	assert.Equal(t, dbMetadata, *log.Metadata, "merge must not override DB-authoritative metadata with the snapshot")
	assert.Equal(t, "user-456", log.MetadataParsed["cortex-user-id"])
}

func TestExtractPayload_NilOrEmptyMetadataOmittedFromSnapshot(t *testing.T) {
	payload := ExtractPayload(&Log{ID: "x"})
	assert.NotContains(t, payload, "metadata", "nil Metadata must not appear in snapshot")

	empty := ""
	payload = ExtractPayload(&Log{ID: "y", Metadata: &empty})
	assert.NotContains(t, payload, "metadata", "empty Metadata must not appear in snapshot")
}

func TestClearPayload_DoesNotTouchIndexFields(t *testing.T) {
	log := &Log{
		ID:           "test-1",
		Provider:     "anthropic",
		Model:        "claude-3",
		Status:       "success",
		InputHistory: `[{"role":"user","content":"hello"}]`,
	}
	ClearPayload(log)
	assert.Equal(t, "test-1", log.ID)
	assert.Equal(t, "anthropic", log.Provider)
	assert.Equal(t, "claude-3", log.Model)
	assert.Equal(t, "success", log.Status)
	assert.Empty(t, log.InputHistory)
}

func TestBuildInputContentSummaryUsesLastTextMessage(t *testing.T) {
	chatUser := "What is the weather?"
	chatAssistant := "It's sunny"
	chatHistoryAssistant := "Earlier assistant answer"
	responsesUser := "生成一张图"
	responsesAssistant := "图片已生成"
	responsesReasoning := "正在处理图片"

	tests := []struct {
		name string
		log  Log
		want string
	}{
		{
			name: "chat output assistant is later than input user",
			log: Log{
				InputHistoryParsed: []schemas.ChatMessage{
					{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &chatUser}},
				},
				OutputMessageParsed: &schemas.ChatMessage{
					Role:    schemas.ChatMessageRoleAssistant,
					Content: &schemas.ChatMessageContent{ContentStr: &chatAssistant},
				},
			},
			want: chatAssistant,
		},
		{
			name: "last assistant in chat history beats earlier user",
			log: Log{
				InputHistoryParsed: []schemas.ChatMessage{
					{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &chatUser}},
					{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: &chatHistoryAssistant}},
				},
			},
			want: chatHistoryAssistant,
		},
		{
			name: "responses output assistant is later than input user",
			log: Log{
				ResponsesInputHistoryParsed: []schemas.ResponsesMessage{responsesMessage(schemas.ResponsesInputMessageRoleUser, responsesUser)},
				ResponsesOutputParsed:       []schemas.ResponsesMessage{responsesMessage(schemas.ResponsesInputMessageRoleAssistant, responsesAssistant)},
			},
			want: responsesAssistant,
		},
		{
			name: "reasoning is fallback when no user or assistant text exists",
			log: Log{
				ResponsesOutputParsed: []schemas.ResponsesMessage{{
					Type:              schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
					ResponsesReasoning: &schemas.ResponsesReasoning{Summary: []schemas.ResponsesReasoningSummary{{Text: responsesReasoning}}},
				}},
			},
			want: reasoningListPreviewPlaceholder,
		},
		{
			name: "later reasoning beats earlier assistant text",
			log: Log{
				ResponsesOutputParsed: []schemas.ResponsesMessage{
					responsesMessage(schemas.ResponsesInputMessageRoleAssistant, responsesAssistant),
					{
						Type:              schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
						ResponsesReasoning: &schemas.ResponsesReasoning{Summary: []schemas.ResponsesReasoningSummary{{Text: responsesReasoning}}},
					},
				},
			},
			want: reasoningListPreviewPlaceholder,
		},
		{
			name: "later assistant text beats earlier reasoning",
			log: Log{
				ResponsesOutputParsed: []schemas.ResponsesMessage{
					{
						Type:              schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
						ResponsesReasoning: &schemas.ResponsesReasoning{Summary: []schemas.ResponsesReasoningSummary{{Text: responsesReasoning}}},
					},
					responsesMessage(schemas.ResponsesInputMessageRoleAssistant, responsesAssistant),
				},
			},
			want: responsesAssistant,
		},
		{
			name: "later output reasoning beats earlier input user text",
			log: Log{
				ResponsesInputHistoryParsed: []schemas.ResponsesMessage{responsesMessage(schemas.ResponsesInputMessageRoleUser, responsesUser)},
				ResponsesOutputParsed: []schemas.ResponsesMessage{{
					Type:              schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
					ResponsesReasoning: &schemas.ResponsesReasoning{Summary: []schemas.ResponsesReasoningSummary{{Text: responsesReasoning}}},
				}},
			},
			want: reasoningListPreviewPlaceholder,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.log.BuildInputContentSummary())
		})
	}
}

func TestBuildInputContentSummaryEncryptedOnlyReasoningReturnsPlaceholder(t *testing.T) {
	encrypted := "enc-bytes"
	chatLog := Log{
		OutputMessageParsed: &schemas.ChatMessage{
			Role: schemas.ChatMessageRoleAssistant,
			ChatAssistantMessage: &schemas.ChatAssistantMessage{
				ReasoningDetails: []schemas.ChatReasoningDetails{{Index: 0, Type: schemas.BifrostReasoningDetailsTypeEncrypted, Data: &encrypted}},
			},
		},
	}
	assert.Equal(t, reasoningListPreviewPlaceholder, chatLog.BuildInputContentSummary())

	responsesLog := Log{
		ResponsesOutputParsed: []schemas.ResponsesMessage{{
			Type:               schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
			ResponsesReasoning: &schemas.ResponsesReasoning{EncryptedContent: &encrypted},
		}},
	}
	assert.Equal(t, reasoningListPreviewPlaceholder, responsesLog.BuildInputContentSummary())
}

func TestTrimListPayloadPreviews(t *testing.T) {
	chatText := "It is sunny."
	reasoningSummary := "**Processing image reference**\n\nI need to check the view_image"

	logs := []Log{
		{
			// chat 行：正文预览与 reasoning 字段都保留
			Object: "chat.completion",
			OutputMessageParsed: &schemas.ChatMessage{
				Role:    schemas.ChatMessageRoleAssistant,
				Content: &schemas.ChatMessageContent{ContentStr: &chatText},
				ChatAssistantMessage: &schemas.ChatAssistantMessage{
					Reasoning: &reasoningSummary,
				},
			},
		},
		{
			// Image Edit 行：剥离 base64 图片，仅保留 prompt
			Object:               "image_edit",
			ImageEditInputParsed: &schemas.ImageEditInput{Images: []schemas.ImageInput{{Image: []byte("base64")}}, Prompt: "把背景换成海边"},
		},
		{
			// realtime.turn 行保持完整
			Object: "realtime.turn",
			OutputMessageParsed: &schemas.ChatMessage{
				Role:    schemas.ChatMessageRoleAssistant,
				Content: &schemas.ChatMessageContent{ContentStr: &chatText},
			},
		},
	}

	trimListPayloadPreviews(logs)

	// 行 0：chat 正文与 reasoning 都保留
	require.NotNil(t, logs[0].OutputMessageParsed)
	require.NotNil(t, logs[0].OutputMessageParsed.Content)
	assert.Equal(t, chatText, *logs[0].OutputMessageParsed.Content.ContentStr)
	require.NotNil(t, logs[0].OutputMessageParsed.ChatAssistantMessage)
	require.NotNil(t, logs[0].OutputMessageParsed.Reasoning)
	assert.Equal(t, reasoningSummary, *logs[0].OutputMessageParsed.Reasoning)

	// 行 1：image edit 只留 prompt
	require.NotNil(t, logs[1].ImageEditInputParsed)
	assert.Nil(t, logs[1].ImageEditInputParsed.Images, "base64 图片应被剥离")
	assert.Equal(t, "把背景换成海边", logs[1].ImageEditInputParsed.Prompt)

	// 行 2：realtime 不动
	assert.NotNil(t, logs[2].OutputMessageParsed, "realtime.turn 行不应被裁剪")
}

func TestBuildTags(t *testing.T) {
	vkID := "vk_123"
	rrID := "rr_456"
	log := &Log{
		Provider:      "anthropic",
		Model:         "claude-3-sonnet",
		Status:        "success",
		Object:        "chat.completion",
		VirtualKeyID:  &vkID,
		SelectedKeyID: "sk_789",
		RoutingRuleID: &rrID,
		Stream:        true,
		Timestamp:     time.Date(2026, 4, 3, 14, 0, 0, 0, time.UTC),
	}

	tags := BuildTags(log)
	assert.Equal(t, "anthropic", tags["provider"])
	assert.Equal(t, "claude-3-sonnet", tags["model"])
	assert.Equal(t, "success", tags["status"])
	assert.Equal(t, "chat.completion", tags["object_type"])
	assert.Equal(t, "vk_123", tags["virtual_key_id"])
	assert.Equal(t, "sk_789", tags["selected_key_id"])
	assert.Equal(t, "rr_456", tags["routing_rule_id"])
	assert.Equal(t, "true", tags["stream"])
	assert.Equal(t, "false", tags["has_error"])
	assert.Equal(t, "2026-04-03", tags["date"])
	assert.LessOrEqual(t, len(tags), 10, "S3 allows max 10 tags")
}

func TestBuildTags_ErrorStatus(t *testing.T) {
	log := &Log{Status: "error", Timestamp: time.Now()}
	tags := BuildTags(log)
	assert.Equal(t, "true", tags["has_error"])
}

func TestObjectKey(t *testing.T) {
	ts := time.Date(2026, 4, 3, 14, 0, 0, 0, time.UTC)
	key := ObjectKey("bifrost", ts, "req_abc123")
	assert.Equal(t, "bifrost/logs/2026/04/03/14/req_abc123.json.gz", key)
}

func TestMCPToolObjectKey(t *testing.T) {
	ts := time.Date(2026, 4, 3, 14, 0, 0, 0, time.UTC)
	key := MCPToolObjectKey("bifrost", ts, "mcp_abc123")
	assert.Equal(t, "bifrost/mcp-logs/2026/04/03/14/mcp_abc123.json.gz", key)
}

func TestMCPToolLogPayload_RoundTripFullLog(t *testing.T) {
	vkID := "vk_123"
	cost := 0.01
	latency := 42.5
	entry := &MCPToolLog{
		ID:             "mcp-1",
		RequestID:      "req-1",
		LLMRequestID:   strPtr("llm-1"),
		Timestamp:      time.Date(2026, 4, 3, 14, 0, 0, 0, time.UTC),
		ToolName:       "search",
		ServerLabel:    "docs",
		VirtualKeyID:   &vkID,
		VirtualKeyName: strPtr("prod-key"),
		Latency:        &latency,
		Cost:           &cost,
		Status:         "success",
		CreatedAt:      time.Date(2026, 4, 3, 14, 0, 1, 0, time.UTC),
		ArgumentsParsed: map[string]any{
			"query": "full input",
		},
		ResultParsed: map[string]any{
			"ok": true,
		},
		ErrorDetailsParsed: &schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Message: "stored for round trip",
			},
		},
		MetadataParsed: map[string]interface{}{
			"trace": "abc",
		},
		PluginLogs: `{"guardrails":[{"plugin_name":"guardrails","level":"info","message":"arguments redacted","timestamp":1}]}`,
		RedactionData: &schemas.RedactionData{
			ReversibleMappings: schemas.RedactionMapsByPhase{Input: map[string]string{"EMAIL-1": "private@example.com"}},
		},
		RedactionMapping: `plain:{"input":{"EMAIL-1":"private@example.com"}}`,
	}

	data, err := MarshalMCPToolLogPayload(entry)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "private@example.com")

	dbEntry := &MCPToolLog{HasObject: true, RedactionMapping: entry.RedactionMapping}
	err = MergeMCPToolLogPayloadFromJSON(dbEntry, data)
	require.NoError(t, err)

	assert.True(t, dbEntry.HasObject)
	assert.Equal(t, entry.ID, dbEntry.ID)
	assert.Equal(t, entry.RequestID, dbEntry.RequestID)
	assert.Equal(t, entry.ToolName, dbEntry.ToolName)
	assert.Equal(t, entry.ServerLabel, dbEntry.ServerLabel)
	assert.Equal(t, entry.Status, dbEntry.Status)
	assert.Equal(t, "full input", dbEntry.ArgumentsParsed.(map[string]interface{})["query"])
	assert.Equal(t, true, dbEntry.ResultParsed.(map[string]interface{})["ok"])
	assert.Equal(t, "stored for round trip", dbEntry.ErrorDetailsParsed.Error.Message)
	assert.Equal(t, "abc", dbEntry.MetadataParsed["trace"])
	assert.Equal(t, entry.PluginLogs, dbEntry.PluginLogs)
	assert.Equal(t, entry.RedactionMapping, dbEntry.RedactionMapping)
	assert.Nil(t, dbEntry.RedactionData)
}

// TestMCPToolLogRedactionMappingJSONVisibility verifies only the authorized virtual mapping is API-visible.
func TestMCPToolLogRedactionMappingJSONVisibility(t *testing.T) {
	entry := &MCPToolLog{
		RedactionMapping: `plain:{"input":{"EMAIL-1":"private@example.com"}}`,
		RevealRedactionMapping: &schemas.RedactionMapsByPhase{
			Input: map[string]string{"EMAIL-1": "revealed@example.com"},
		},
	}

	data, err := sonic.Marshal(entry)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "private@example.com")
	assert.Contains(t, string(data), `"redaction_mapping":{"input":{"EMAIL-1":"revealed@example.com"}}`)
}

func TestPrepareMCPToolDBEntry_KeepsOnlyInputPreview(t *testing.T) {
	longInput := ""
	for i := 0; i < 260; i++ {
		longInput += "a"
	}
	entry := &MCPToolLog{
		ID:          "mcp-preview",
		RequestID:   "req-preview",
		Timestamp:   time.Date(2026, 4, 3, 14, 0, 0, 0, time.UTC),
		ToolName:    "echo",
		ServerLabel: "local",
		Status:      "success",
		ArgumentsParsed: map[string]any{
			"input": longInput,
		},
		ResultParsed: map[string]any{
			"secret": "large result",
		},
		ErrorDetailsParsed: &schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Message: "large error",
			},
		},
		MetadataParsed: map[string]interface{}{
			"trace": "abc",
		},
		PluginLogs: `{"guardrails":[{"message":"arguments redacted"}]}`,
	}

	PrepareMCPToolDBEntry(entry)

	assert.Equal(t, "mcp-preview", entry.ID)
	assert.Equal(t, "req-preview", entry.RequestID)
	assert.Equal(t, "echo", entry.ToolName)
	assert.Equal(t, "local", entry.ServerLabel)
	assert.Empty(t, entry.Result)
	assert.Empty(t, entry.ErrorDetails)
	assert.Nil(t, entry.ResultParsed)
	assert.Nil(t, entry.ErrorDetailsParsed)
	assert.NotEmpty(t, entry.Arguments)
	assert.NotEmpty(t, entry.Metadata)
	assert.Equal(t, `{"guardrails":[{"message":"arguments redacted"}]}`, entry.PluginLogs)

	var preview string
	require.NoError(t, sonic.Unmarshal([]byte(entry.Arguments), &preview))
	assert.Len(t, []rune(preview), 200)
	assert.Contains(t, preview, `"input":`)
}

func TestPayloadFieldNames(t *testing.T) {
	fields := PayloadFieldNames()
	assert.True(t, len(fields) > 0)
	// Verify it's a copy.
	fields[0] = "modified"
	assert.NotEqual(t, "modified", payloadFields[0])
}

func TestMergePayloadFromJSON_RebuildsDualColumns(t *testing.T) {
	entry := newContentSummarySplitFixture()
	require.NoError(t, entry.SerializeFields())
	payload := ExtractPayload(entry)
	data, err := MarshalPayload(payload)
	require.NoError(t, err)
	ClearPayload(entry)
	entry.ContentSummary = ""
	entry.ListPreview = ""
	require.NoError(t, MergePayloadFromJSON(entry, data))
	for _, marker := range []string{"split-first-alpha", "split-second-beta", "split-resp-gamma", "split-resp-delta", "split-answer-epsilon"} {
		assert.Contains(t, entry.ContentSummary, marker)
	}
	assert.Equal(t, "split-answer-epsilon", entry.ListPreview)
}

func strPtr(s string) *string {
	return &s
}
