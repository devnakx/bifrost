package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	loggingplugin "github.com/maximhq/bifrost/plugins/logging"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// 辅助：解析 getLogMessages 响应，校验五字段存在性与类型
type messagesWireResp struct {
	Messages []json.RawMessage `json:"messages"`
	Offset   int               `json:"offset"`
	Limit    int               `json:"limit"`
	Total    int               `json:"total"`
	HasMore  bool              `json:"has_more"`
}

func doGetMessages(t *testing.T, h *LoggingHandler, id string, offset, limit *int) (int, json.RawMessage) {
	return doGetMessagesWithOrder(t, h, id, offset, limit, "")
}

func doGetMessagesWithOrder(t *testing.T, h *LoggingHandler, id string, offset, limit *int, order string) (int, json.RawMessage) {
	return doGetMessagesWithOrderAndRoles(t, h, id, offset, limit, order, "")
}

func doGetMessagesWithOrderAndRoles(t *testing.T, h *LoggingHandler, id string, offset, limit *int, order, roles string) (int, json.RawMessage) {
	t.Helper()
	uri := fmt.Sprintf("/api/logs/%s/messages", id)
	q := []string{}
	if offset != nil {
		q = append(q, fmt.Sprintf("offset=%d", *offset))
	}
	if limit != nil {
		q = append(q, fmt.Sprintf("limit=%d", *limit))
	}
	if order != "" {
		q = append(q, "order="+order)
	}
	if roles != "" {
		q = append(q, "roles="+roles)
	}
	if len(q) > 0 {
		uri += "?" + strings.Join(q, "&")
	}
	var req fasthttp.Request
	req.SetRequestURI(uri)
	ctx := &fasthttp.RequestCtx{}
	ctx.Init(&req, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}, nil)
	ctx.SetUserValue("id", id)
	h.getLogMessages(ctx)
	return ctx.Response.StatusCode(), ctx.Response.Body()
}

func TestGetLogMessages_Wire_DescendingPagination(t *testing.T) {
	h, store := newMessagesWireFixture(t)
	chatID, _, _ := newMixedLogFixture(t, store)

	code, body := doGetMessagesWithOrder(t, h, chatID, intPtr(0), intPtr(20), "desc")
	require.Equal(t, fasthttp.StatusOK, code)
	firstPage := mustUnmarshalMessagesWire(t, body)
	assert.Equal(t, 45, firstPage.Total)
	assert.Len(t, firstPage.Messages, 20)
	assert.Contains(t, string(firstPage.Messages[0]), "chat-output")
	assert.Contains(t, string(firstPage.Messages[1]), "chat-43")
	assert.True(t, firstPage.HasMore)

	code, body = doGetMessagesWithOrder(t, h, chatID, intPtr(40), intPtr(20), "desc")
	require.Equal(t, fasthttp.StatusOK, code)
	lastPage := mustUnmarshalMessagesWire(t, body)
	assert.Len(t, lastPage.Messages, 5)
	assert.Contains(t, string(lastPage.Messages[0]), "chat-04")
	assert.Contains(t, string(lastPage.Messages[4]), "chat-00")
	assert.False(t, lastPage.HasMore)
}

func mustUnmarshalMessagesWire(t *testing.T, body json.RawMessage) messagesWireResp {
	t.Helper()
	var m map[string]json.RawMessage
	require.NoError(t, sonic.Unmarshal(body, &m), "wire JSON 需合法")
	for _, k := range []string{"messages", "offset", "limit", "total", "has_more"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("wire 缺字段 %q body=%s", k, string(body))
		}
	}
	var resp messagesWireResp
	require.NoError(t, sonic.Unmarshal(body, &resp))
	return resp
}

func intPtr(v int) *int { return &v }

// 构造 45 条 chat 消息：22 条 input_history + 1 条 output_message 的变体（通过多次混合 responses 构造更大序列）
// 为覆盖 MUST DO 的瘦投影三轮 20+ 混合，这里分三类 Log 覆盖 chat/responses 混用
func newMixedLogFixture(t *testing.T, store logstore.LogStore) (chatID, respID, mixedID string) {
	t.Helper()
	ctx := context.Background()

	// chat: 34 input + 1 output = 35（与现有 TestGetLogMessages_Pagination 一致量级，但这里扩展到 44 以便翻页拼接）
	var chatInput []schemas.ChatMessage
	for i := 0; i < 44; i++ {
		c := fmt.Sprintf("chat-%02d", i)
		cc := c
		chatInput = append(chatInput, schemas.ChatMessage{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &cc}})
	}
	out := "chat-output"
	chatLog := &logstore.Log{
		ID:                  "wire-chat-44",
		Provider:            "openai",
		Model:               "gpt-4o-mini",
		Object:              "chat.completion",
		Status:              "success",
		Timestamp:           nowUTC(),
		InputHistoryParsed:  chatInput,
		OutputMessageParsed: &schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: &out}},
		RawRequest:          `{"big":"chat"}`,
		RawResponse:         `{"big":"chat-resp"}`,
	}
	require.NoError(t, chatLog.SerializeFields())
	require.NoError(t, store.Create(ctx, chatLog))

	// responses: 30 input + 12 output = 42
	var respIn []schemas.ResponsesMessage
	for i := 0; i < 30; i++ {
		c := fmt.Sprintf("resp-in-%02d", i)
		cc := c
		role := schemas.ResponsesInputMessageRoleUser
		respIn = append(respIn, schemas.ResponsesMessage{Role: &role, Content: &schemas.ResponsesMessageContent{ContentStr: &cc}})
	}
	var respOut []schemas.ResponsesMessage
	for i := 0; i < 12; i++ {
		c := fmt.Sprintf("resp-out-%02d", i)
		cc := c
		role := schemas.ResponsesInputMessageRoleAssistant
		respOut = append(respOut, schemas.ResponsesMessage{Role: &role, Content: &schemas.ResponsesMessageContent{ContentStr: &cc}})
	}
	respLog := &logstore.Log{
		ID:                          "wire-responses-42",
		Provider:                    "openai",
		Model:                       "gpt-4o",
		Object:                      "response",
		Status:                      "success",
		Timestamp:                   nowUTC(),
		ResponsesInputHistoryParsed: respIn,
		ResponsesOutputParsed:       respOut,
		RawRequest:                  `{"big":"resp"}`,
		RawResponse:                 `{"big":"resp-resp"}`,
	}
	require.NoError(t, respLog.SerializeFields())
	require.NoError(t, store.Create(ctx, respLog))

	// mixed: 用 store.Create 额外写入一个 chat 以便覆盖数据库中同时存在 responses 列为空的语义
	mixedIn := chatInput[:22]
	mixedOut := "mixed-output"
	mixedLog := &logstore.Log{
		ID:                  "wire-mixed-23",
		Provider:            "openai",
		Model:               "gpt-4o-mini",
		Object:              "chat.completion",
		Status:              "success",
		Timestamp:           nowUTC(),
		InputHistoryParsed:  mixedIn,
		OutputMessageParsed: &schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: &mixedOut}},
	}
	require.NoError(t, mixedLog.SerializeFields())
	require.NoError(t, store.Create(ctx, mixedLog))

	return "wire-chat-44", "wire-responses-42", "wire-mixed-23"
}

func nowUTC() (t time.Time) { return timeNow().UTC() }

var timeNow = func() time.Time { return time.Now() }

func newMessagesWireFixture(t *testing.T) (*LoggingHandler, logstore.LogStore) {
	t.Helper()
	ctx := context.Background()
	store, err := logstore.NewLogStore(ctx, &logstore.Config{
		Enabled: true,
		Type:    logstore.LogStoreTypeSQLite,
		Config:  &logstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "wire.db")},
	}, testLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close(ctx) })
	plugin, err := loggingplugin.Init(ctx, &loggingplugin.Config{}, testLogger{}, store, nil, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = plugin.Cleanup() })
	h := NewLoggingHandler(plugin.GetPluginLogManager(), &logDetailTestRedactedStub{}, &lib.Config{})
	return h, store
}

// (a) walk + wire 契约：真实 sqlite + 3 轮混用构造，经 PluginLogManager → getLogMessages 的 fasthttp 调用，
// 断言五字段存在且类型正确；offset=0,limit=20 与 offset=20,limit=20 拼接后与全量 SliceLogMessages 一致；has_more 首屏 true、末页 false
func TestGetLogMessages_Wire_FiveFieldsAndPagingStitch(t *testing.T) {
	h, store := newMessagesWireFixture(t)
	chatID, respID, _ := newMixedLogFixture(t, store)
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		id   string
		total int
	}{
		{"chat 45 条", chatID, 45},
		{"responses 42 条", respID, 42},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// 全量基准：直接读 store 做 SliceLogMessages
			fullLog, err := store.FindByID(ctx, tc.id)
			require.NoError(t, err)
			fullMsgs, fullTotal, err := logstore.SliceLogMessages(fullLog, 0, 100)
			require.NoError(t, err)
			require.Equal(t, tc.total, fullTotal)
			require.Len(t, fullMsgs, tc.total)

			// wire 首屏
			code, body := doGetMessages(t, h, tc.id, intPtr(0), intPtr(20))
			require.Equal(t, fasthttp.StatusOK, code)
			r0 := mustUnmarshalMessagesWire(t, body)
			assert.Equal(t, 0, r0.Offset)
			assert.Equal(t, 20, r0.Limit)
			assert.Equal(t, tc.total, r0.Total)
			assert.Len(t, r0.Messages, 20)
			assert.True(t, r0.HasMore, "非末页 has_more 应 true")

			// wire 次屏
			code, body = doGetMessages(t, h, tc.id, intPtr(20), intPtr(20))
			require.Equal(t, fasthttp.StatusOK, code)
			r1 := mustUnmarshalMessagesWire(t, body)
			assert.Equal(t, 20, r1.Offset)
			assert.Equal(t, 20, r1.Limit)
			assert.Equal(t, tc.total, r1.Total)
			assert.Len(t, r1.Messages, 20)
			// 42 时次屏仍非末页，45 时次屏仍非末页
			assert.True(t, r1.HasMore)

			// 末页：offset=40 对 45 总量应 5 条且 has_more false；对 42 总量应 2 条且 false
			code, body = doGetMessages(t, h, tc.id, intPtr(40), intPtr(20))
			require.Equal(t, fasthttp.StatusOK, code)
			r2 := mustUnmarshalMessagesWire(t, body)
			assert.Equal(t, tc.total, r2.Total)
			assert.False(t, r2.HasMore, "末页 has_more 应 false")
			expectedLast := tc.total - 40
			assert.Len(t, r2.Messages, expectedLast)

			// 拼接校验：三屏拼接与全量一致
			stitched := append(append([]json.RawMessage{}, r0.Messages...), r1.Messages...)
			stitched = append(stitched, r2.Messages...)
			require.Len(t, stitched, tc.total)
			for i := range stitched {
				assert.Equal(t, string(fullMsgs[i]), string(stitched[i]), "拼接第 %d 条不一致", i)
			}

			// 类型校验：messages 每条为 object，offset/limit/total 为 number，has_more 为 boolean（已由 sonic 反序列化保证）
			var raw map[string]json.RawMessage
			require.NoError(t, sonic.Unmarshal(body, &raw))
			// has_more 必须是 true/false 字面量而非字符串
			hm := strings.TrimSpace(string(raw["has_more"]))
			assert.True(t, hm == "true" || hm == "false", "has_more 应为 boolean，实际 %s", hm)
		})
	}
}

// (b) 默认分页：缺省 offset/limit 时 wire 返回 offset=0 limit=20
func TestGetLogMessages_Wire_DefaultLimit(t *testing.T) {
	h, store := newMessagesWireFixture(t)
	chatID, _, _ := newMixedLogFixture(t, store)
	code, body := doGetMessages(t, h, chatID, nil, nil)
	require.Equal(t, fasthttp.StatusOK, code)
	r := mustUnmarshalMessagesWire(t, body)
	assert.Equal(t, 0, r.Offset)
	assert.Equal(t, 20, r.Limit)
	assert.Equal(t, 45, r.Total)
	assert.Len(t, r.Messages, 20)
	assert.True(t, r.HasMore)
}

// (c) has_more 边界：恰满页与溢出页
func TestGetLogMessages_Wire_HasMore_Boundaries(t *testing.T) {
	h, store := newMessagesWireFixture(t)
	chatID, _, _ := newMixedLogFixture(t, store)
	// 45 总量，limit 20 时 offset=20 已在第二页末非末页末页间，offset=40 为末页
	cases := []struct {
		offset int
		limit  int
		more   bool
		n      int
	}{
		{0, 20, true, 20},
		{20, 20, true, 20},
		{40, 20, false, 5},
		{45, 20, false, 0},
		{100, 20, false, 0},
	}
	for _, tc := range cases {
		code, body := doGetMessages(t, h, chatID, intPtr(tc.offset), intPtr(tc.limit))
		require.Equal(t, fasthttp.StatusOK, code)
		r := mustUnmarshalMessagesWire(t, body)
		assert.Equal(t, tc.more, r.HasMore, "offset=%d limit=%d has_more", tc.offset, tc.limit)
		assert.Len(t, r.Messages, tc.n, "offset=%d limit=%d len", tc.offset, tc.limit)
	}
}

// (d) 回退 wire 等价：不实现 FindByIDMessagesProjection 的 stub 与真实投影在 messages 分页内容上一致
type noProjectionStore struct {
	logstore.LogStore
}

func (s *noProjectionStore) FindByIDMessagesProjection(_ context.Context, _ string) (*logstore.Log, error) {
	return nil, fmt.Errorf("not implemented")
}

type fallbackLogManager struct {
	store logstore.LogStore
}

func (m *fallbackLogManager) Search(ctx context.Context, f *logstore.SearchFilters, p *logstore.PaginationOptions) (*logstore.SearchResult, error) {
	return nil, nil
}
func (m *fallbackLogManager) GetLog(ctx context.Context, id string) (*logstore.Log, error) {
	return m.store.FindByID(ctx, id)
}
func (m *fallbackLogManager) GetLogDetail(ctx context.Context, id string) (*logstore.Log, error) {
	return m.store.FindByID(ctx, id)
}
func (m *fallbackLogManager) GetLogMessagesPayload(ctx context.Context, id string) (*logstore.Log, error) {
	// 刻意回退到 FindByID，不走投影
	return m.store.FindByID(ctx, id)
}
func (m *fallbackLogManager) GetSessionLogs(_ context.Context, _ string, _ *logstore.PaginationOptions) (*logstore.SessionDetailResult, error) {
	return nil, nil
}
func (m *fallbackLogManager) GetSessionSummary(_ context.Context, _ string) (*logstore.SessionSummaryResult, error) {
	return nil, nil
}
func (m *fallbackLogManager) GetStats(_ context.Context, _ *logstore.SearchFilters) (*logstore.SearchStats, error) {
	return nil, nil
}
func (m *fallbackLogManager) GetHistogram(_ context.Context, _ *logstore.SearchFilters, _ int64) (*logstore.HistogramResult, error) {
	return nil, nil
}
func (m *fallbackLogManager) GetTokenHistogram(_ context.Context, _ *logstore.SearchFilters, _ int64) (*logstore.TokenHistogramResult, error) {
	return nil, nil
}
func (m *fallbackLogManager) GetCostHistogram(_ context.Context, _ *logstore.SearchFilters, _ int64) (*logstore.CostHistogramResult, error) {
	return nil, nil
}
func (m *fallbackLogManager) GetModelHistogram(_ context.Context, _ *logstore.SearchFilters, _ int64) (*logstore.ModelHistogramResult, error) {
	return nil, nil
}
func (m *fallbackLogManager) GetLatencyHistogram(_ context.Context, _ *logstore.SearchFilters, _ int64) (*logstore.LatencyHistogramResult, error) {
	return nil, nil
}
func (m *fallbackLogManager) GetProviderCostHistogram(_ context.Context, _ *logstore.SearchFilters, _ int64) (*logstore.ProviderCostHistogramResult, error) {
	return nil, nil
}
func (m *fallbackLogManager) GetProviderTokenHistogram(_ context.Context, _ *logstore.SearchFilters, _ int64) (*logstore.ProviderTokenHistogramResult, error) {
	return nil, nil
}
func (m *fallbackLogManager) GetProviderLatencyHistogram(_ context.Context, _ *logstore.SearchFilters, _ int64) (*logstore.ProviderLatencyHistogramResult, error) {
	return nil, nil
}
func (m *fallbackLogManager) GetThroughputHistogram(_ context.Context, _ *logstore.SearchFilters, _ int64) (*logstore.ThroughputHistogramResult, error) {
	return nil, nil
}
func (m *fallbackLogManager) GetProviderThroughputHistogram(_ context.Context, _ *logstore.SearchFilters, _ int64) (*logstore.ProviderThroughputHistogramResult, error) {
	return nil, nil
}
func (m *fallbackLogManager) GetModelRankings(_ context.Context, _ *logstore.SearchFilters) (*logstore.ModelRankingResult, error) {
	return nil, nil
}
func (m *fallbackLogManager) GetDimensionRankings(_ context.Context, _ *logstore.SearchFilters, _ logstore.RankingDimension) (*logstore.DimensionRankingResult, error) {
	return nil, nil
}
func (m *fallbackLogManager) GetDroppedRequests(_ context.Context) int64 { return 0 }
func (m *fallbackLogManager) GetAvailableModels(_ context.Context, _ int, _ string) ([]string, error) {
	return nil, nil
}
func (m *fallbackLogManager) GetAvailableAliases(_ context.Context, _ int, _ string) ([]string, error) {
	return nil, nil
}
func (m *fallbackLogManager) GetAvailableSelectedKeys(_ context.Context, _ int, _ string) ([]loggingplugin.KeyPair, error) {
	return nil, nil
}
func (m *fallbackLogManager) GetAvailableVirtualKeys(_ context.Context, _ int, _ string) ([]loggingplugin.KeyPair, error) {
	return nil, nil
}
func (m *fallbackLogManager) GetAvailableRoutingRules(_ context.Context, _ int, _ string) ([]loggingplugin.KeyPair, error) {
	return nil, nil
}
func (m *fallbackLogManager) GetAvailableRoutingEngines(_ context.Context, _ int, _ string) ([]string, error) {
	return nil, nil
}
func (m *fallbackLogManager) GetAvailableStopReasons(_ context.Context, _ int, _ string) ([]string, error) {
	return nil, nil
}
func (m *fallbackLogManager) GetAvailableUserAgents(_ context.Context, _ int, _ string) ([]string, error) {
	return nil, nil
}
func (m *fallbackLogManager) GetAvailableApps(_ context.Context, _ int, _ string) ([]string, error) {
	return nil, nil
}
func (m *fallbackLogManager) GetAvailableTeams(_ context.Context, _ int, _ string) ([]loggingplugin.KeyPair, error) {
	return nil, nil
}
func (m *fallbackLogManager) GetAvailableCustomers(_ context.Context, _ int, _ string) ([]loggingplugin.KeyPair, error) {
	return nil, nil
}
func (m *fallbackLogManager) GetAvailableUsers(_ context.Context, _ int, _ string) ([]loggingplugin.KeyPair, error) {
	return nil, nil
}
func (m *fallbackLogManager) GetAvailableProjects(_ context.Context, _ int, _ string) ([]loggingplugin.KeyPair, error) {
	return nil, nil
}
func (m *fallbackLogManager) GetAvailableToolCallNames(_ context.Context, _ int, _ string) ([]string, error) {
	return nil, nil
}
func (m *fallbackLogManager) GetAvailableBusinessUnits(_ context.Context, _ int, _ string) ([]loggingplugin.KeyPair, error) {
	return nil, nil
}
func (m *fallbackLogManager) GetAvailableMetadataKeys(_ context.Context, _ int, _ string) (map[string][]string, error) {
	return nil, nil
}
func (m *fallbackLogManager) GetDimensionCostHistogram(_ context.Context, _ *logstore.SearchFilters, _ int64, _ logstore.HistogramDimension) (*logstore.DimensionCostHistogramResult, error) {
	return nil, nil
}
func (m *fallbackLogManager) GetDimensionTokenHistogram(_ context.Context, _ *logstore.SearchFilters, _ int64, _ logstore.HistogramDimension) (*logstore.DimensionTokenHistogramResult, error) {
	return nil, nil
}
func (m *fallbackLogManager) GetDimensionLatencyHistogram(_ context.Context, _ *logstore.SearchFilters, _ int64, _ logstore.HistogramDimension) (*logstore.DimensionLatencyHistogramResult, error) {
	return nil, nil
}
func (m *fallbackLogManager) DeleteLog(_ context.Context, _ string) error { return nil }
func (m *fallbackLogManager) DeleteLogs(_ context.Context, _ []string) error { return nil }
func (m *fallbackLogManager) RecalculateCosts(_ context.Context, _ *logstore.SearchFilters, _ int) (*loggingplugin.RecalculateCostResult, error) {
	return nil, nil
}
func (m *fallbackLogManager) RecalculateCostsWithProgress(_ context.Context, _ *logstore.SearchFilters, _ int, _ func(loggingplugin.RecalculateCostProgress)) (*loggingplugin.RecalculateCostResult, error) {
	return nil, nil
}
func (m *fallbackLogManager) BuildCostRecalcJobMeta(_ context.Context, _ logstore.SearchFilters, _ bool) (string, error) {
	return "{}", nil
}
func (m *fallbackLogManager) RunCostRecalcJob(_ context.Context, metaJSON string, _ func(string) error) (string, error) {
	return metaJSON, nil
}
func (m *fallbackLogManager) GetMCPToolLog(_ context.Context, _ string) (*logstore.MCPToolLog, error) {
	return nil, nil
}
func (m *fallbackLogManager) SearchMCPToolLogs(_ context.Context, _ *logstore.MCPToolLogSearchFilters, _ *logstore.PaginationOptions) (*logstore.MCPToolLogSearchResult, error) {
	return nil, nil
}
func (m *fallbackLogManager) GetMCPToolLogStats(_ context.Context, _ *logstore.MCPToolLogSearchFilters) (*logstore.MCPToolLogStats, error) {
	return nil, nil
}
func (m *fallbackLogManager) GetAvailableToolNames(_ context.Context, _ int, _ string) ([]string, error) {
	return nil, nil
}
func (m *fallbackLogManager) GetAvailableServerLabels(_ context.Context, _ int, _ string) ([]string, error) {
	return nil, nil
}
func (m *fallbackLogManager) GetAvailableMCPUserAgents(_ context.Context, _ int, _ string) ([]string, error) {
	return nil, nil
}
func (m *fallbackLogManager) GetAvailableMCPApps(_ context.Context, _ int, _ string) ([]string, error) {
	return nil, nil
}
func (m *fallbackLogManager) GetAvailableMCPVirtualKeys(_ context.Context, _ int, _ string) ([]loggingplugin.KeyPair, error) {
	return nil, nil
}
func (m *fallbackLogManager) GetMCPHistogram(_ context.Context, _ logstore.MCPToolLogSearchFilters, _ int64) (*logstore.MCPHistogramResult, error) {
	return nil, nil
}
func (m *fallbackLogManager) GetMCPCostHistogram(_ context.Context, _ logstore.MCPToolLogSearchFilters, _ int64) (*logstore.MCPCostHistogramResult, error) {
	return nil, nil
}
func (m *fallbackLogManager) GetMCPTopTools(_ context.Context, _ logstore.MCPToolLogSearchFilters, _ int) (*logstore.MCPTopToolsResult, error) {
	return nil, nil
}
func (m *fallbackLogManager) DeleteMCPToolLogs(_ context.Context, _ []string) error { return nil }
func (m *fallbackLogManager) ListUserAgentMappings(_ context.Context) ([]logstore.UserAgentMapping, error) {
	return nil, nil
}
func (m *fallbackLogManager) CreateUserAgentMapping(_ context.Context, _ *logstore.UserAgentMapping) (*logstore.UserAgentMapping, error) {
	return nil, nil
}
func (m *fallbackLogManager) UpdateUserAgentMapping(_ context.Context, _ string, _ *logstore.UserAgentMapping) (*logstore.UserAgentMapping, error) {
	return nil, nil
}
func (m *fallbackLogManager) DeleteUserAgentMapping(_ context.Context, _ string) error { return nil }

func TestGetLogMessages_Wire_FallbackEquivalence(t *testing.T) {
	// 同一 sqlite 数据，分别经快路径与回退路径的 getLogMessages，wire 分页内容一致
	ctx := context.Background()
	store, err := logstore.NewLogStore(ctx, &logstore.Config{
		Enabled: true,
		Type:    logstore.LogStoreTypeSQLite,
		Config:  &logstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "fallback-wire.db")},
	}, testLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close(ctx) })
	// 写入 chat 45 条（与 (a) 同规模，便于等价）
	var chatInput []schemas.ChatMessage
	for i := 0; i < 44; i++ {
		c := fmt.Sprintf("fb-%02d", i)
		cc := c
		chatInput = append(chatInput, schemas.ChatMessage{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &cc}})
	}
	out := "fb-output"
	chatLog := &logstore.Log{
		ID:                  "wire-fallback-1",
		Provider:            "openai",
		Model:               "gpt-4o-mini",
		Object:              "chat.completion",
		Status:              "success",
		Timestamp:           nowUTC(),
		InputHistoryParsed:  chatInput,
		OutputMessageParsed: &schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: &out}},
	}
	require.NoError(t, chatLog.SerializeFields())
	require.NoError(t, store.Create(ctx, chatLog))

	// 快路径 handler（真实 plugin）
	plugin, err := loggingplugin.Init(ctx, &loggingplugin.Config{}, testLogger{}, store, nil, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = plugin.Cleanup() })
	hFast := NewLoggingHandler(plugin.GetPluginLogManager(), &logDetailTestRedactedStub{}, &lib.Config{})
	// 回退 handler（显式 FindByID 回退的 LogManager）
	hFallback := NewLoggingHandler(&fallbackLogManager{store: store}, &logDetailTestRedactedStub{}, &lib.Config{})

	for _, off := range []int{0, 20, 40} {
		c0, b0 := doGetMessages(t, hFast, "wire-fallback-1", intPtr(off), intPtr(20))
		c1, b1 := doGetMessages(t, hFallback, "wire-fallback-1", intPtr(off), intPtr(20))
		require.Equal(t, c0, c1, "offset=%d status", off)
		r0 := mustUnmarshalMessagesWire(t, b0)
		r1 := mustUnmarshalMessagesWire(t, b1)
		assert.Equal(t, r0.Total, r1.Total, "offset=%d total", off)
		assert.Equal(t, r0.Offset, r1.Offset)
		assert.Equal(t, r0.Limit, r1.Limit)
		assert.Equal(t, r0.HasMore, r1.HasMore, "offset=%d has_more", off)
		require.Len(t, r1.Messages, len(r0.Messages), "offset=%d len", off)
		for i := range r0.Messages {
			assert.Equal(t, string(r0.Messages[i]), string(r1.Messages[i]), "offset=%d idx=%d", off, i)
		}
	}
}

func TestGetLogMessages_Wire_RolesFilter(t *testing.T) {
	h, store := newMessagesWireFixture(t)
	chatID, _, _ := newMixedLogFixture(t, store)

	code, body := doGetMessagesWithOrderAndRoles(t, h, chatID, intPtr(0), intPtr(20), "", "user")
	require.Equal(t, fasthttp.StatusOK, code)
	r := mustUnmarshalMessagesWire(t, body)
	assert.Equal(t, 44, r.Total)
	assert.Len(t, r.Messages, 20)
	assert.True(t, r.HasMore)
	assert.Contains(t, string(r.Messages[0]), "chat-00")

	code, body = doGetMessagesWithOrderAndRoles(t, h, chatID, intPtr(0), intPtr(20), "", "assistant")
	require.Equal(t, fasthttp.StatusOK, code)
	r = mustUnmarshalMessagesWire(t, body)
	assert.Equal(t, 1, r.Total)
	assert.Len(t, r.Messages, 1)
	assert.Contains(t, string(r.Messages[0]), "chat-output")
	assert.False(t, r.HasMore)

	code, body = doGetMessagesWithOrderAndRoles(t, h, chatID, intPtr(0), intPtr(20), "", "user,assistant")
	require.Equal(t, fasthttp.StatusOK, code)
	r = mustUnmarshalMessagesWire(t, body)
	assert.Equal(t, 45, r.Total)
	assert.Len(t, r.Messages, 20)
	assert.True(t, r.HasMore)

	code, body = doGetMessagesWithOrderAndRoles(t, h, chatID, intPtr(0), intPtr(20), "", "system,user,assistant,tool,reasoning")
	require.Equal(t, fasthttp.StatusOK, code)
	r = mustUnmarshalMessagesWire(t, body)
	assert.Equal(t, 45, r.Total)
	assert.Len(t, r.Messages, 20)

	code, body = doGetMessagesWithOrderAndRoles(t, h, chatID, intPtr(0), intPtr(20), "desc", "user")
	require.Equal(t, fasthttp.StatusOK, code)
	r = mustUnmarshalMessagesWire(t, body)
	assert.Equal(t, 44, r.Total)
	assert.Contains(t, string(r.Messages[0]), "chat-43")

	code, body = doGetMessagesWithOrderAndRoles(t, h, chatID, intPtr(40), intPtr(20), "", "user")
	require.Equal(t, fasthttp.StatusOK, code)
	r = mustUnmarshalMessagesWire(t, body)
	assert.Equal(t, 44, r.Total)
	assert.Len(t, r.Messages, 4)
	assert.False(t, r.HasMore)

	code, _ = doGetMessagesWithOrderAndRoles(t, h, chatID, intPtr(0), intPtr(20), "", "foo")
	require.Equal(t, fasthttp.StatusBadRequest, code)
}
