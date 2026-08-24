package handlers

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	configstoretables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	loggingplugin "github.com/maximhq/bifrost/plugins/logging"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

type logDetailTestRedactedStub struct{}

func (s *logDetailTestRedactedStub) GetAllRedactedKeys(ctx context.Context, ids []string) []schemas.Key {
	return nil
}
func (s *logDetailTestRedactedStub) GetAllRedactedVirtualKeys(ctx context.Context, ids []string) []configstoretables.TableVirtualKey {
	return nil
}
func (s *logDetailTestRedactedStub) GetAllRedactedRoutingRules(ctx context.Context, ids []string) []configstoretables.TableRoutingRule {
	return nil
}

func newLogDetailFixture(t *testing.T) (*LoggingHandler, logstore.LogStore) {
	t.Helper()
	ctx := context.Background()
	store, err := logstore.NewLogStore(ctx, &logstore.Config{
		Enabled: true,
		Type:    logstore.LogStoreTypeSQLite,
		Config:  &logstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "detail.db")},
	}, testLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close(ctx) })
	plugin, err := loggingplugin.Init(ctx, &loggingplugin.Config{}, testLogger{}, store, nil, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = plugin.Cleanup() })
	handler := NewLoggingHandler(plugin.GetPluginLogManager(), &logDetailTestRedactedStub{}, &lib.Config{})
	return handler, store
}

func TestGetLogByID_SlimmedExcludesHeavyFields(t *testing.T) {
	h, store := newLogDetailFixture(t)
	ctx := context.Background()

	rawReq := `{"model":"gpt-4","messages":[]}`
	rawResp := `{"id":"chatcmpl-123"}`
	respHist := []schemas.ResponsesMessage{
		{Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser), Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("user hi")}},
	}
	respOut := []schemas.ResponsesMessage{
		{Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant), Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("assistant hi")}},
	}
	speechInput := &schemas.SpeechInput{Input: "hello speech"}
	transIn := &schemas.TranscriptionInput{}
	imgEditIn := &schemas.ImageEditInput{Prompt: "edit me"}
	entry := &logstore.Log{
		ID:                          "detail-slim-1",
		Timestamp:                   time.Now().UTC(),
		Provider:                    "openai",
		Model:                       "gpt-4o-mini",
		Object:                      "chat.completion",
		Status:                      "success",
		ResponsesInputHistoryParsed: respHist,
		ResponsesOutputParsed:       respOut,
		RawRequest:                  rawReq,
		RawResponse:                 rawResp,
		SpeechInputParsed:           speechInput,
		TranscriptionInputParsed:    transIn,
		ImageEditInputParsed:        imgEditIn,
		EmbeddingOutputParsed:       []schemas.EmbeddingData{{Index: 0, Object: "embedding", Embedding: schemas.EmbeddingStruct{EmbeddingArray: []float64{1, 2, 3}}}},
		RerankOutputParsed:          []schemas.RerankResult{{Index: 0, RelevanceScore: 0.9}},
		OCROutputParsed:             &schemas.BifrostOCRResponse{Model: "ocr-model"},
		ListModelsOutputParsed:      []schemas.Model{{ID: "m1"}},
		TokenUsageParsed:            &schemas.BifrostLLMUsage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
		ErrorDetailsParsed:          &schemas.BifrostError{IsBifrostError: true, Error: &schemas.ErrorField{Message: "oops"}},
		RoutingEngineLogs:           "engine log",
		AttemptTrailParsed:          []schemas.KeyAttemptRecord{{KeyID: "k1"}},
		OverheadBreakdownParsed:     []logstore.OverheadBucket{{Name: "core", Kind: "core", DurationUs: 10}},
		ParamsParsed:                map[string]interface{}{"temperature": 0.7},
	}
	entry.InputHistoryParsed = []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}}}
	require.NoError(t, entry.SerializeFields())
	entry.ResponsesInputHistory = `[{"role":"user","content":"user hi"}]`
	entry.ResponsesOutput = `[{"role":"assistant","content":"assistant hi"}]`
	entry.RawRequest = rawReq
	entry.RawResponse = rawResp
	require.NoError(t, store.Create(ctx, entry))

	var req fasthttp.Request
	req.SetRequestURI("/api/logs/detail-slim-1")
	reqCtx := &fasthttp.RequestCtx{}
	reqCtx.Init(&req, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}, nil)
	reqCtx.SetUserValue("id", "detail-slim-1")

	h.getLogByID(reqCtx)
	require.Equal(t, fasthttp.StatusOK, reqCtx.Response.StatusCode())
	body := reqCtx.Response.Body()
	var m map[string]json.RawMessage
	require.NoError(t, sonic.Unmarshal(body, &m))
	assert.NotContains(t, m, "responses_input_history", "responses_input_history should be excluded from detail metadata")
	assert.NotContains(t, m, "responses_output")
	assert.NotContains(t, m, "raw_request")
	assert.NotContains(t, m, "raw_response")
	assert.Contains(t, m, "speech_input", "speech_input must be retained")
	assert.Contains(t, m, "image_edit_input")
	assert.Contains(t, m, "embedding_output")
	assert.Contains(t, m, "rerank_output")
	assert.Contains(t, m, "ocr_output")
	assert.Contains(t, m, "list_models_output")
	assert.Contains(t, m, "token_usage")
	assert.Contains(t, m, "error_details")
	assert.Contains(t, m, "routing_engine_logs")
	assert.Contains(t, m, "attempt_trail")
	assert.Contains(t, m, "overhead_breakdown")
	assert.Contains(t, m, "params")
}

func TestGetLogMessages_Pagination(t *testing.T) {
	h, store := newLogDetailFixture(t)
	ctx := context.Background()

	var input []schemas.ChatMessage
	for i := 0; i < 34; i++ {
		c := "msg-" + strings.Repeat("x", 5) + "-" + string(rune(i))
		input = append(input, schemas.ChatMessage{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &c}})
	}
	out := &schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("final output")}}
	entry := &logstore.Log{
		ID:                  "msg-35",
		Timestamp:           time.Now().UTC(),
		Provider:            "openai",
		Model:               "gpt-4",
		Object:              "chat.completion",
		Status:              "success",
		InputHistoryParsed:  input,
		OutputMessageParsed: out,
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, store.Create(ctx, entry))

	cases := []struct {
		name      string
		offset    string
		limit     string
		wantLen   int
		wantMore  bool
		wantTotal int
	}{
		{"first page", "0", "20", 20, true, 35},
		{"second page", "20", "20", 15, false, 35},
		{"overflow", "999", "20", 0, false, 35},
		{"default limit", "", "", 20, true, 35},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var req fasthttp.Request
			uri := "/api/logs/msg-35/messages"
			q := ""
			if tc.offset != "" || tc.limit != "" {
				parts := []string{}
				if tc.offset != "" {
					parts = append(parts, "offset="+tc.offset)
				}
				if tc.limit != "" {
					parts = append(parts, "limit="+tc.limit)
				}
				q = "?" + strings.Join(parts, "&")
			}
			req.SetRequestURI(uri + q)
			reqCtx := &fasthttp.RequestCtx{}
			reqCtx.Init(&req, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}, nil)
			reqCtx.SetUserValue("id", "msg-35")
			h.getLogMessages(reqCtx)
			require.Equal(t, fasthttp.StatusOK, reqCtx.Response.StatusCode())
			var resp struct {
				Messages []json.RawMessage `json:"messages"`
				Offset   int               `json:"offset"`
				Limit    int               `json:"limit"`
				Total    int               `json:"total"`
				HasMore  bool              `json:"has_more"`
			}
			require.NoError(t, sonic.Unmarshal(reqCtx.Response.Body(), &resp))
			assert.Equal(t, tc.wantLen, len(resp.Messages))
			assert.Equal(t, tc.wantMore, resp.HasMore)
			assert.Equal(t, tc.wantTotal, resp.Total)
		})
	}
}

type staticLogRedactionResolver struct {
	mapping *schemas.RedactionMapsByPhase
	calls   int
}

func (r *staticLogRedactionResolver) ResolveLogRedactionMapping(_ *fasthttp.RequestCtx, _ *logstore.Log) (*schemas.RedactionMapsByPhase, error) {
	r.calls++
	return r.mapping, nil
}

func TestGetLogRaw_ReturnsMapping(t *testing.T) {
	h, store := newLogDetailFixture(t)
	ctx := context.Background()
	rawReq := `{"model":"gpt-4"}`
	rawResp := `{"id":"resp-123"}`
	entry := &logstore.Log{
		ID:               "raw-1",
		Timestamp:        time.Now().UTC(),
		Provider:         "openai",
		Model:            "gpt-4",
		Object:           "chat.completion",
		Status:           "success",
		RawRequest:       rawReq,
		RawResponse:      rawResp,
		RedactionMapping: `plain:{"input":{"EMAIL-1":"a@b.com"}}`,
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, store.Create(ctx, entry))

	revealed := &schemas.RedactionMapsByPhase{Input: map[string]string{"EMAIL-1": "revealed@example.com"}}
	resolver := &staticLogRedactionResolver{mapping: revealed}
	h.SetLogRedactionMappingResolver(resolver)

	var req fasthttp.Request
	req.SetRequestURI("/api/logs/raw-1/raw")
	reqCtx := &fasthttp.RequestCtx{}
	reqCtx.Init(&req, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}, nil)
	reqCtx.SetUserValue("id", "raw-1")
	h.getLogRaw(reqCtx)
	require.Equal(t, fasthttp.StatusOK, reqCtx.Response.StatusCode())
	var resp struct {
		RawRequest       string                        `json:"raw_request"`
		RawResponse      string                        `json:"raw_response"`
		RedactionMapping *schemas.RedactionMapsByPhase `json:"redaction_mapping"`
	}
	require.NoError(t, sonic.Unmarshal(reqCtx.Response.Body(), &resp))
	assert.Equal(t, rawReq, resp.RawRequest)
	assert.Equal(t, rawResp, resp.RawResponse)
	require.NotNil(t, resp.RedactionMapping)
	assert.Equal(t, "revealed@example.com", resp.RedactionMapping.Input["EMAIL-1"])
}

func TestGetLogMessages_NotFoundPreserves404(t *testing.T) {
	h, _ := newLogDetailFixture(t)
	var req fasthttp.Request
	req.SetRequestURI("/api/logs/not-exist/messages")
	reqCtx := &fasthttp.RequestCtx{}
	reqCtx.Init(&req, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}, nil)
	reqCtx.SetUserValue("id", "not-exist")
	h.getLogMessages(reqCtx)
	assert.Equal(t, fasthttp.StatusNotFound, reqCtx.Response.StatusCode())
}
