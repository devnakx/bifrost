package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	loggingplugin "github.com/maximhq/bifrost/plugins/logging"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// countMock 追踪 Search 的 SkipCount 与调用次数，用于 8 场景校验
type countMock struct {
	mu            sync.Mutex
	totalToReturn int64
	calls         []logstore.PaginationOptions
	lastFilters   *logstore.SearchFilters
	deleteCalls   int
	searchCalls   int
	// logsToReturn 非空时 Search 原样返回该行集，用于列表字段装配校验
	logsToReturn []logstore.Log
}

func newCountMock(total int64) *countMock {
	return &countMock{totalToReturn: total}
}

func (m *countMock) Search(_ context.Context, filters *logstore.SearchFilters, pagination *logstore.PaginationOptions) (*logstore.SearchResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.searchCalls++
	cp := *pagination
	m.calls = append(m.calls, cp)
	cf := *filters
	m.lastFilters = &cf
	if m.logsToReturn != nil {
		return &logstore.SearchResult{
			Logs:       m.logsToReturn,
			Pagination: logstore.PaginationOptions{Limit: pagination.Limit, Offset: pagination.Offset, SortBy: pagination.SortBy, Order: pagination.Order, TotalCount: int64(len(m.logsToReturn))},
			Stats:      logstore.SearchStats{TotalRequests: int64(len(m.logsToReturn))},
			HasLogs:    true,
		}, nil
	}
	// 模拟 SkipCount 时 COUNT 被跳过返回 0
	if pagination.SkipCount {
		return &logstore.SearchResult{
			Logs:       []logstore.Log{},
			Pagination: logstore.PaginationOptions{Limit: pagination.Limit, Offset: pagination.Offset, SortBy: pagination.SortBy, Order: pagination.Order, TotalCount: 0},
			Stats:      logstore.SearchStats{TotalRequests: 0},
			HasLogs:    true,
		}, nil
	}
	return &logstore.SearchResult{
		Logs:       []logstore.Log{},
		Pagination: logstore.PaginationOptions{Limit: pagination.Limit, Offset: pagination.Offset, SortBy: pagination.SortBy, Order: pagination.Order, TotalCount: m.totalToReturn},
		Stats:      logstore.SearchStats{TotalRequests: m.totalToReturn},
		HasLogs:    true,
	}, nil
}

func (m *countMock) callsSnapshot() []logstore.PaginationOptions {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]logstore.PaginationOptions, len(m.calls))
	copy(out, m.calls)
	return out
}

func (m *countMock) GetLog(_ context.Context, _ string) (*logstore.Log, error) { return nil, nil }
func (m *countMock) GetLogDetail(_ context.Context, _ string) (*logstore.Log, error) {
	return nil, nil
}
func (m *countMock) GetLogMessagesPayload(_ context.Context, _ string) (*logstore.Log, error) {
	return nil, nil
}
func (m *countMock) GetSessionLogs(_ context.Context, _ string, _ *logstore.PaginationOptions) (*logstore.SessionDetailResult, error) {
	return nil, nil
}

func (m *countMock) GetSessionSummary(_ context.Context, _ string) (*logstore.SessionSummaryResult, error) {
	return nil, nil
}
func (m *countMock) GetStats(_ context.Context, _ *logstore.SearchFilters) (*logstore.SearchStats, error) {
	return &logstore.SearchStats{}, nil
}
func (m *countMock) GetHistogram(_ context.Context, _ *logstore.SearchFilters, _ int64) (*logstore.HistogramResult, error) {
	return &logstore.HistogramResult{}, nil
}
func (m *countMock) GetTokenHistogram(_ context.Context, _ *logstore.SearchFilters, _ int64) (*logstore.TokenHistogramResult, error) {
	return &logstore.TokenHistogramResult{}, nil
}
func (m *countMock) GetCostHistogram(_ context.Context, _ *logstore.SearchFilters, _ int64) (*logstore.CostHistogramResult, error) {
	return &logstore.CostHistogramResult{}, nil
}
func (m *countMock) GetModelHistogram(_ context.Context, _ *logstore.SearchFilters, _ int64) (*logstore.ModelHistogramResult, error) {
	return &logstore.ModelHistogramResult{}, nil
}
func (m *countMock) GetLatencyHistogram(_ context.Context, _ *logstore.SearchFilters, _ int64) (*logstore.LatencyHistogramResult, error) {
	return &logstore.LatencyHistogramResult{}, nil
}
func (m *countMock) GetProviderCostHistogram(_ context.Context, _ *logstore.SearchFilters, _ int64) (*logstore.ProviderCostHistogramResult, error) {
	return &logstore.ProviderCostHistogramResult{}, nil
}
func (m *countMock) GetProviderTokenHistogram(_ context.Context, _ *logstore.SearchFilters, _ int64) (*logstore.ProviderTokenHistogramResult, error) {
	return &logstore.ProviderTokenHistogramResult{}, nil
}
func (m *countMock) GetProviderLatencyHistogram(_ context.Context, _ *logstore.SearchFilters, _ int64) (*logstore.ProviderLatencyHistogramResult, error) {
	return &logstore.ProviderLatencyHistogramResult{}, nil
}
func (m *countMock) GetThroughputHistogram(_ context.Context, _ *logstore.SearchFilters, _ int64) (*logstore.ThroughputHistogramResult, error) {
	return &logstore.ThroughputHistogramResult{}, nil
}
func (m *countMock) GetProviderThroughputHistogram(_ context.Context, _ *logstore.SearchFilters, _ int64) (*logstore.ProviderThroughputHistogramResult, error) {
	return &logstore.ProviderThroughputHistogramResult{}, nil
}
func (m *countMock) GetModelRankings(_ context.Context, _ *logstore.SearchFilters) (*logstore.ModelRankingResult, error) {
	return &logstore.ModelRankingResult{}, nil
}
func (m *countMock) GetDimensionRankings(_ context.Context, _ *logstore.SearchFilters, _ logstore.RankingDimension) (*logstore.DimensionRankingResult, error) {
	return &logstore.DimensionRankingResult{}, nil
}
func (m *countMock) GetDroppedRequests(_ context.Context) int64 { return 0 }
func (m *countMock) GetAvailableModels(_ context.Context, _ int, _ string) ([]string, error) {
	return nil, nil
}
func (m *countMock) GetAvailableAliases(_ context.Context, _ int, _ string) ([]string, error) {
	return nil, nil
}
func (m *countMock) GetAvailableSelectedKeys(_ context.Context, _ int, _ string) ([]loggingplugin.KeyPair, error) {
	return nil, nil
}
func (m *countMock) GetAvailableVirtualKeys(_ context.Context, _ int, _ string) ([]loggingplugin.KeyPair, error) {
	return nil, nil
}
func (m *countMock) GetAvailableRoutingRules(_ context.Context, _ int, _ string) ([]loggingplugin.KeyPair, error) {
	return nil, nil
}
func (m *countMock) GetAvailableRoutingEngines(_ context.Context, _ int, _ string) ([]string, error) {
	return nil, nil
}
func (m *countMock) GetAvailableStopReasons(_ context.Context, _ int, _ string) ([]string, error) {
	return nil, nil
}
func (m *countMock) GetAvailableUserAgents(_ context.Context, _ int, _ string) ([]string, error) {
	return nil, nil
}
func (m *countMock) GetAvailableApps(_ context.Context, _ int, _ string) ([]string, error) {
	return nil, nil
}
func (m *countMock) GetAvailableTeams(_ context.Context, _ int, _ string) ([]loggingplugin.KeyPair, error) {
	return nil, nil
}
func (m *countMock) GetAvailableCustomers(_ context.Context, _ int, _ string) ([]loggingplugin.KeyPair, error) {
	return nil, nil
}
func (m *countMock) GetAvailableUsers(_ context.Context, _ int, _ string) ([]loggingplugin.KeyPair, error) {
	return nil, nil
}
func (m *countMock) GetAvailableProjects(_ context.Context, _ int, _ string) ([]loggingplugin.KeyPair, error) {
	return nil, nil
}
func (m *countMock) GetAvailableToolCallNames(_ context.Context, _ int, _ string) ([]string, error) {
	return nil, nil
}
func (m *countMock) GetAvailableBusinessUnits(_ context.Context, _ int, _ string) ([]loggingplugin.KeyPair, error) {
	return nil, nil
}
func (m *countMock) GetAvailableMetadataKeys(_ context.Context, _ int, _ string) (map[string][]string, error) {
	return nil, nil
}
func (m *countMock) GetDimensionCostHistogram(_ context.Context, _ *logstore.SearchFilters, _ int64, _ logstore.HistogramDimension) (*logstore.DimensionCostHistogramResult, error) {
	return nil, nil
}
func (m *countMock) GetDimensionTokenHistogram(_ context.Context, _ *logstore.SearchFilters, _ int64, _ logstore.HistogramDimension) (*logstore.DimensionTokenHistogramResult, error) {
	return nil, nil
}
func (m *countMock) GetDimensionLatencyHistogram(_ context.Context, _ *logstore.SearchFilters, _ int64, _ logstore.HistogramDimension) (*logstore.DimensionLatencyHistogramResult, error) {
	return nil, nil
}
func (m *countMock) DeleteLog(_ context.Context, _ string) error { return nil }
func (m *countMock) DeleteLogs(_ context.Context, _ []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleteCalls++
	return nil
}
func (m *countMock) RecalculateCosts(_ context.Context, _ *logstore.SearchFilters, _ int) (*loggingplugin.RecalculateCostResult, error) {
	return &loggingplugin.RecalculateCostResult{}, nil
}
func (m *countMock) RecalculateCostsWithProgress(_ context.Context, _ *logstore.SearchFilters, _ int, _ func(loggingplugin.RecalculateCostProgress)) (*loggingplugin.RecalculateCostResult, error) {
	return nil, nil
}
func (m *countMock) BuildCostRecalcJobMeta(_ context.Context, _ logstore.SearchFilters, _ bool) (string, error) {
	return "{}", nil
}
func (m *countMock) RunCostRecalcJob(_ context.Context, metaJSON string, _ func(string) error) (string, error) {
	return metaJSON, nil
}
func (m *countMock) GetMCPToolLog(_ context.Context, _ string) (*logstore.MCPToolLog, error) {
	return nil, nil
}
func (m *countMock) SearchMCPToolLogs(_ context.Context, _ *logstore.MCPToolLogSearchFilters, _ *logstore.PaginationOptions) (*logstore.MCPToolLogSearchResult, error) {
	return nil, nil
}
func (m *countMock) GetMCPToolLogStats(_ context.Context, _ *logstore.MCPToolLogSearchFilters) (*logstore.MCPToolLogStats, error) {
	return nil, nil
}
func (m *countMock) GetAvailableToolNames(_ context.Context, _ int, _ string) ([]string, error) {
	return nil, nil
}
func (m *countMock) GetAvailableServerLabels(_ context.Context, _ int, _ string) ([]string, error) {
	return nil, nil
}
func (m *countMock) GetAvailableMCPUserAgents(_ context.Context, _ int, _ string) ([]string, error) {
	return nil, nil
}
func (m *countMock) GetAvailableMCPApps(_ context.Context, _ int, _ string) ([]string, error) {
	return nil, nil
}
func (m *countMock) GetAvailableMCPVirtualKeys(_ context.Context, _ int, _ string) ([]loggingplugin.KeyPair, error) {
	return nil, nil
}
func (m *countMock) GetMCPHistogram(_ context.Context, _ logstore.MCPToolLogSearchFilters, _ int64) (*logstore.MCPHistogramResult, error) {
	return &logstore.MCPHistogramResult{}, nil
}
func (m *countMock) GetMCPCostHistogram(_ context.Context, _ logstore.MCPToolLogSearchFilters, _ int64) (*logstore.MCPCostHistogramResult, error) {
	return &logstore.MCPCostHistogramResult{}, nil
}
func (m *countMock) GetMCPTopTools(_ context.Context, _ logstore.MCPToolLogSearchFilters, _ int) (*logstore.MCPTopToolsResult, error) {
	return &logstore.MCPTopToolsResult{}, nil
}
func (m *countMock) DeleteMCPToolLogs(_ context.Context, _ []string) error { return nil }
func (m *countMock) ListUserAgentMappings(_ context.Context) ([]logstore.UserAgentMapping, error) {
	return nil, nil
}
func (m *countMock) CreateUserAgentMapping(_ context.Context, _ *logstore.UserAgentMapping) (*logstore.UserAgentMapping, error) {
	return nil, nil
}
func (m *countMock) UpdateUserAgentMapping(_ context.Context, _ string, _ *logstore.UserAgentMapping) (*logstore.UserAgentMapping, error) {
	return nil, nil
}
func (m *countMock) DeleteUserAgentMapping(_ context.Context, _ string) error { return nil }

// doGetLogs 发起一次 GET /api/logs 请求并解析响应
func doGetLogs(t *testing.T, h *LoggingHandler, uri string, userID string, roleID uint) (*fasthttp.RequestCtx, logstore.SearchResult) {
	t.Helper()
	var req fasthttp.Request
	req.SetRequestURI(uri)
	ctx := &fasthttp.RequestCtx{}
	ctx.Init(&req, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}, nil)
	if userID != "" {
		ctx.SetUserValue(schemas.BifrostContextKeyUserID, userID)
		ctx.SetUserValue(schemas.BifrostContextKeyUserRoleID, roleID)
	}
	h.getLogs(ctx)
	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("getLogs %q 返回 %d: %s", uri, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
	var result logstore.SearchResult
	if err := json.Unmarshal(ctx.Response.Body(), &result); err != nil {
		t.Fatalf("解析响应失败: %v body=%s", err, string(ctx.Response.Body()))
	}
	return ctx, result
}

func doDeleteLogs(t *testing.T, h *LoggingHandler, ids []string) *fasthttp.RequestCtx {
	t.Helper()
	var req fasthttp.Request
	req.Header.SetMethod(fasthttp.MethodDelete)
	req.SetRequestURI("/api/logs")
	body, _ := json.Marshal(map[string]interface{}{"ids": ids})
	req.SetBody(body)
	req.Header.SetContentType("application/json")
	ctx := &fasthttp.RequestCtx{}
	ctx.Init(&req, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}, nil)
	h.deleteLogs(ctx)
	return ctx
}

func logsCountCacheSize(h *LoggingHandler) int {
	h.logsCountCache.mu.Lock()
	defer h.logsCountCache.mu.Unlock()
	return len(h.logsCountCache.entries)
}

// (a) 同参 period=24h 连打两次第二次 SkipCount==true 且 total 一致
func TestLogsCountCache_Hit_SamePeriod(t *testing.T) {
	SetLogger(&mockLogger{})
	mock := newCountMock(42)
	h := NewLoggingHandler(mock, &logsListCapRedactedKeysStub{}, &lib.Config{})
	_, r1 := doGetLogs(t, h, "/api/logs?period=24h", "", 0)
	calls1 := mock.callsSnapshot()
	if len(calls1) != 1 || calls1[0].SkipCount {
		t.Fatalf("(a) 第一次应 SkipCount=false，实际 %#v", calls1)
	}
	if r1.Pagination.TotalCount != 42 || r1.Stats.TotalRequests != 42 {
		t.Fatalf("(a) 第一次 total 期望 42，实际 pagination=%d stats=%d", r1.Pagination.TotalCount, r1.Stats.TotalRequests)
	}
	_, r2 := doGetLogs(t, h, "/api/logs?period=24h", "", 0)
	calls2 := mock.callsSnapshot()
	if len(calls2) != 2 || !calls2[1].SkipCount {
		t.Fatalf("(a) 第二次应 SkipCount=true，实际 %#v", calls2)
	}
	if r2.Pagination.TotalCount != 42 || r2.Stats.TotalRequests != 42 {
		t.Fatalf("(a) 第二次 total 应与第一次一致 42，实际 pagination=%d stats=%d", r2.Pagination.TotalCount, r2.Stats.TotalRequests)
	}
}

// (b) period=24h vs 7d 各自独立不污染
func TestLogsCountCache_Isolation_DifferentPeriod(t *testing.T) {
	SetLogger(&mockLogger{})
	mock := newCountMock(99)
	h := NewLoggingHandler(mock, &logsListCapRedactedKeysStub{}, &lib.Config{})
	doGetLogs(t, h, "/api/logs?period=24h", "", 0)
	doGetLogs(t, h, "/api/logs?period=7d", "", 0)
	calls := mock.callsSnapshot()
	if len(calls) != 2 {
		t.Fatalf("(b) 前两次应各一次 COUNT，实际 %d", len(calls))
	}
	if calls[0].SkipCount || calls[1].SkipCount {
		t.Fatalf("(b) 不同 period 首两次均应 SkipCount=false，实际 %#v", calls)
	}
	_, r3 := doGetLogs(t, h, "/api/logs?period=24h", "", 0)
	calls = mock.callsSnapshot()
	if len(calls) != 3 || !calls[2].SkipCount {
		t.Fatalf("(b) 第三次 24h 应命中首次缓存 SkipCount=true，实际 %#v", calls)
	}
	if r3.Pagination.TotalCount != 99 {
		t.Fatalf("(b) 第三次 total 应为 99，实际 %d", r3.Pagination.TotalCount)
	}
	// 7d 仍独立命中
	_, r4 := doGetLogs(t, h, "/api/logs?period=7d", "", 0)
	calls = mock.callsSnapshot()
	if !calls[3].SkipCount {
		t.Fatalf("(b) 第四次 7d 应命中 SkipCount=true，实际 %#v", calls)
	}
	if r4.Pagination.TotalCount != 99 {
		t.Fatalf("(b) 第四次 total 应为 99，实际 %d", r4.Pagination.TotalCount)
	}
}

// (c) offset/limit 不同但 filters 相同共享同一计数
func TestLogsCountCache_ShareAcrossPagination(t *testing.T) {
	SetLogger(&mockLogger{})
	mock := newCountMock(55)
	h := NewLoggingHandler(mock, &logsListCapRedactedKeysStub{}, &lib.Config{})
	_, r1 := doGetLogs(t, h, "/api/logs?period=24h&limit=10&offset=0", "", 0)
	calls := mock.callsSnapshot()
	if calls[0].SkipCount {
		t.Fatalf("(c) 第一次应 SkipCount=false")
	}
	_, r2 := doGetLogs(t, h, "/api/logs?period=24h&limit=20&offset=20", "", 0)
	calls = mock.callsSnapshot()
	if !calls[1].SkipCount {
		t.Fatalf("(c) 第二次不同分页应命中同一计数 SkipCount=true，实际 %#v", calls[1])
	}
	if r1.Pagination.TotalCount != r2.Pagination.TotalCount || r2.Pagination.TotalCount != 55 {
		t.Fatalf("(c) 两次 total 应一致 55，实际 %d vs %d", r1.Pagination.TotalCount, r2.Pagination.TotalCount)
	}
	// sort/order 也不应影响 key
	_, r3 := doGetLogs(t, h, "/api/logs?period=24h&limit=5&offset=0&sort_by=latency&order=asc", "", 0)
	calls = mock.callsSnapshot()
	if !calls[2].SkipCount {
		t.Fatalf("(c) sort/order 不同仍应命中")
	}
	if r3.Pagination.TotalCount != 55 {
		t.Fatalf("(c) 第三次 total 应 55，实际 %d", r3.Pagination.TotalCount)
	}
	_ = fmt.Sprintf("%v", r1) // 占位避免未使用
}

// (d) 含 content_search 每次都执行 COUNT
func TestLogsCountCache_BypassContentSearch(t *testing.T) {
	SetLogger(&mockLogger{})
	mock := newCountMock(77)
	h := NewLoggingHandler(mock, &logsListCapRedactedKeysStub{}, &lib.Config{})
	doGetLogs(t, h, "/api/logs?period=24h&content_search=hello", "", 0)
	doGetLogs(t, h, "/api/logs?period=24h&content_search=hello", "", 0)
	calls := mock.callsSnapshot()
	if len(calls) != 2 {
		t.Fatalf("(d) 含 content_search 应两次均走 COUNT，实际 %d", len(calls))
	}
	for i, c := range calls {
		if c.SkipCount {
			t.Fatalf("(d) 第 %d 次含 content_search 不应 SkipCount", i+1)
		}
	}
	// 不含 content_search 的同参应正常缓存一次
	doGetLogs(t, h, "/api/logs?period=24h", "", 0)
	doGetLogs(t, h, "/api/logs?period=24h", "", 0)
	calls = mock.callsSnapshot()
	// 前两次 content_search + 后两次普通 = 4 次，其中最后一次应 SkipCount
	if !calls[3].SkipCount {
		t.Fatalf("(d) 普通查询第二次应命中")
	}
}

// (e) 调用方身份不同不同享
func TestLogsCountCache_IdentityPartition(t *testing.T) {
	SetLogger(&mockLogger{})
	mock := newCountMock(33)
	h := NewLoggingHandler(mock, &logsListCapRedactedKeysStub{}, &lib.Config{})
	doGetLogs(t, h, "/api/logs?period=24h", "alice", 2)
	doGetLogs(t, h, "/api/logs?period=24h", "bob", 2)
	calls := mock.callsSnapshot()
	if len(calls) != 2 || calls[1].SkipCount {
		t.Fatalf("(e) 不同用户应不共享，第二次应 SkipCount=false，实际 %#v", calls)
	}
	// 同用户同角色应命中
	doGetLogs(t, h, "/api/logs?period=24h", "alice", 2)
	calls = mock.callsSnapshot()
	if !calls[2].SkipCount {
		t.Fatalf("(e) 同身份第二次应命中")
	}
	// 同用户不同角色应不共享
	doGetLogs(t, h, "/api/logs?period=24h", "alice", 1)
	calls = mock.callsSnapshot()
	if calls[3].SkipCount {
		t.Fatalf("(e) 同用户不同角色应不共享，实际 SkipCount=true")
	}
}

// (f) 锁配对压测 1000 次同参连打后 entries map size <=2
func TestLogsCountCache_Concurrency_OrphanProtection(t *testing.T) {
	SetLogger(&mockLogger{})
	mock := newCountMock(11)
	h := NewLoggingHandler(mock, &logsListCapRedactedKeysStub{}, &lib.Config{})
	var wg sync.WaitGroup
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var req fasthttp.Request
			req.SetRequestURI("/api/logs?period=24h")
			ctx := &fasthttp.RequestCtx{}
			ctx.Init(&req, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}, nil)
			h.getLogs(ctx)
		}()
	}
	wg.Wait()
	size := logsCountCacheSize(h)
	if size > 2 {
		t.Fatalf("(f) 1000 次同参并发后 entries size 期望 <=2，实际 %d", size)
	}
}

// (g) deleteLogs 后残留<=1 且后续重新 COUNT
func TestLogsCountCache_DeletePurges(t *testing.T) {
	SetLogger(&mockLogger{})
	mock := newCountMock(66)
	h := NewLoggingHandler(mock, &logsListCapRedactedKeysStub{}, &lib.Config{})
	doGetLogs(t, h, "/api/logs?period=24h", "", 0)
	doGetLogs(t, h, "/api/logs?period=7d", "", 0)
	if size := logsCountCacheSize(h); size != 2 {
		t.Fatalf("(g) 预置两条缓存后 size 应 2，实际 %d", size)
	}
	ctx := doDeleteLogs(t, h, []string{"a"})
	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("(g) deleteLogs 期望 200，实际 %d %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
	if size := logsCountCacheSize(h); size > 1 {
		t.Fatalf("(g) delete 后残留期望 <=1，实际 %d", size)
	}
	// 后续同参应重新 COUNT（SkipCount=false）
	doGetLogs(t, h, "/api/logs?period=24h", "", 0)
	calls := mock.callsSnapshot()
	// 前两次缓存 + 删除后一次应为 SkipCount=false
	last := calls[len(calls)-1]
	if last.SkipCount {
		t.Fatalf("(g) 删除后首次查询应重新 COUNT SkipCount=false，实际 true")
	}
}

// (h) 显式 start_time/end_time 同参命中
func TestLogsCountCache_ExplicitTimeWindow(t *testing.T) {
	SetLogger(&mockLogger{})
	mock := newCountMock(88)
	h := NewLoggingHandler(mock, &logsListCapRedactedKeysStub{}, &lib.Config{})
	start := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339Nano)
	end := time.Now().UTC().Format(time.RFC3339Nano)
	uri := fmt.Sprintf("/api/logs?start_time=%s&end_time=%s", start, end)
	_, r1 := doGetLogs(t, h, uri, "", 0)
	calls := mock.callsSnapshot()
	if calls[0].SkipCount {
		t.Fatalf("(h) 显式时间窗第一次应 SkipCount=false")
	}
	_, r2 := doGetLogs(t, h, uri, "", 0)
	calls = mock.callsSnapshot()
	if !calls[1].SkipCount {
		t.Fatalf("(h) 显式时间窗第二次应命中 SkipCount=true，实际 %#v", calls[1])
	}
	if r1.Pagination.TotalCount != r2.Pagination.TotalCount || r2.Pagination.TotalCount != 88 {
		t.Fatalf("(h) 两次 total 应一致 88，实际 %d vs %d", r1.Pagination.TotalCount, r2.Pagination.TotalCount)
	}
}

// TestGetLogs_ListRowsCarryPreviewWithoutFullText 列表行携带 list_preview 且不含 content_summary 全文
func TestGetLogs_ListRowsCarryPreviewWithoutFullText(t *testing.T) {
	SetLogger(&mockLogger{})
	mock := newCountMock(1)
	mock.logsToReturn = []logstore.Log{{
		ID:             "split-list-1",
		Provider:       "openai",
		Model:          "gpt-4o-mini",
		Object:         "chat.completion",
		Status:         "success",
		ContentSummary: "split-full-alpha split-full-beta split-full-gamma",
		ListPreview:    "split-answer-epsilon",
	}}
	h := NewLoggingHandler(mock, &logsListCapRedactedKeysStub{}, &lib.Config{})
	var req fasthttp.Request
	req.SetRequestURI("/api/logs?limit=5")
	reqCtx := &fasthttp.RequestCtx{}
	reqCtx.Init(&req, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}, nil)
	h.getLogs(reqCtx)
	require.Equal(t, fasthttp.StatusOK, reqCtx.Response.StatusCode())
	var wire map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(reqCtx.Response.Body(), &wire))
	var rows []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(wire["logs"], &rows))
	require.Len(t, rows, 1)
	assert.NotContains(t, rows[0], "content_summary", "列表行不得携带 content_summary 全文")
	require.Contains(t, rows[0], "list_preview")
	var preview string
	require.NoError(t, json.Unmarshal(rows[0]["list_preview"], &preview))
	assert.Equal(t, "split-answer-epsilon", preview)
}
