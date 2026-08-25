package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/logstore"
	loggingplugin "github.com/maximhq/bifrost/plugins/logging"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormLibLogger "gorm.io/gorm/logger"
)

// logscounthandlercache_test.go — getLogs 计数 TTL 缓存的 gorm Trace SQL 级独立证据
//
// logscountcache_test.go 已在 mock LogManager 层验证 SkipCount 传递语义；本文件换一条
// 完全独立的观测通道：真实 sqlite RDBLogStore + 真实 logging 插件 LogManager，用自定义
// gorm logger 按 SELECT count(*) FROM logs 前缀计数，证明 5 次同参请求只产生 1 次 COUNT。

// sqlCountRecorder 记录 gorm Trace 观测到的每条 SQL 文本，供按前缀断言计数
type sqlCountRecorder struct {
	mu   sync.Mutex
	sqls []string
}

func (r *sqlCountRecorder) LogMode(level gormLibLogger.LogLevel) gormLibLogger.Interface { return r }
func (r *sqlCountRecorder) Info(context.Context, string, ...interface{})                 {}
func (r *sqlCountRecorder) Warn(context.Context, string, ...interface{})                 {}
func (r *sqlCountRecorder) Error(context.Context, string, ...interface{})                {}
func (r *sqlCountRecorder) Trace(_ context.Context, _ time.Time, fc func() (string, int64), err error) {
	if err != nil {
		return
	}
	sql, _ := fc()
	r.mu.Lock()
	r.sqls = append(r.sqls, sql)
	r.mu.Unlock()
}

// countObserved 返回以 prefix 开头的已执行 SQL 条数（gorm Trace 的 sql 不含参数占位符替换后的值）
func (r *sqlCountRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.sqls))
	copy(out, r.sqls)
	return out
}

// newLogsCountSQLFixture 构建带自定义 gorm logger 的真实 sqlite RDBLogStore handler 组合
// 与 logslistinputcap_test.go 同构：真实 store → 真实插件 LogManager → LoggingHandler
func newLogsCountSQLFixture(t *testing.T) (*LoggingHandler, logstore.LogStore, *sqlCountRecorder) {
	t.Helper()
	ctx := context.Background()

	recorder := &sqlCountRecorder{}
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "logs.db")), &gorm.Config{
		Logger: recorder,
	})
	require.NoError(t, err)
	// 真实 schema：AutoMigrate 建 logs 及关联表，RDBLogStore 直构注入自定义 gorm logger
	require.NoError(t, db.AutoMigrate(&logstore.Log{}))
	store := logstore.NewLogStoreWithDB(db, testLogger{})

	plugin, err := loggingplugin.Init(ctx, &loggingplugin.Config{}, testLogger{}, store, nil, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = plugin.Cleanup() })

	handler := NewLoggingHandler(plugin.GetPluginLogManager(), &logsListCapRedactedKeysStub{}, &lib.Config{})
	return handler, store, recorder
}

// TestGetLogsSingleCOUNTViaGormTrace period=24h 同参连打 5 次，gorm Trace 只见 1 次日志表 COUNT，
// 且 5 次响应 total 全部一致
func TestGetLogsSingleCOUNTViaGormTrace(t *testing.T) {
	h, store, recorder := newLogsCountSQLFixture(t)
	ctx := context.Background()

	// 合成 7 行日志全部落在 period=24h 窗口内，期望 total=7
	now := time.Now().UTC()
	for i := 0; i < 7; i++ {
		entry := &logstore.Log{
			ID:        fmt.Sprintf("logs-count-sql-%d", i),
			Timestamp: now.Add(-time.Duration(i+1) * time.Minute),
			Object:    "chat.completion",
			Provider:  "openai",
			Model:     "gpt-4o-mini",
			Status:    "success",
		}
		require.NoError(t, entry.SerializeFields())
		require.NoError(t, store.Create(ctx, entry))
	}

	// 记录请求前基线，排除 seed 阶段 SQL，只统计 5 次 getLogs 自身产生的 COUNT
	baseline := len(recorder.snapshot())

	totals := make([]int64, 0, 5)
	for round := 0; round < 5; round++ {
		var req fasthttp.Request
		req.SetRequestURI("/api/logs?period=24h&limit=50")
		reqCtx := &fasthttp.RequestCtx{}
		reqCtx.Init(&req, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}, nil)

		h.getLogs(reqCtx)
		require.Equal(t, fasthttp.StatusOK, reqCtx.Response.StatusCode(),
			"第 %d 轮 GET /api/logs 应返回 200：%s", round+1, string(reqCtx.Response.Body()))

		var resp struct {
			Pagination struct {
				TotalCount int64 `json:"total_count"`
			} `json:"pagination"`
			Stats struct {
				TotalRequests int64 `json:"total_requests"`
			} `json:"stats"`
		}
		require.NoError(t, json.Unmarshal(reqCtx.Response.Body(), &resp))
		require.Equal(t, int64(7), resp.Pagination.TotalCount,
			"第 %d 轮 total_count 应为 7", round+1)
		require.Equal(t, resp.Pagination.TotalCount, resp.Stats.TotalRequests,
			"第 %d 轮 stats.total_requests 应与 total_count 一致", round+1)
		totals = append(totals, resp.Pagination.TotalCount)
	}

	// 核心断言：5 次请求只产生 1 次 COUNT SQL（基线之后的 Trace 才算数，限定 logs 表）
	countSQLs := 0
	for _, sql := range recorder.snapshot()[baseline:] {
		lower := strings.ToLower(strings.TrimSpace(sql))
		if strings.Contains(lower, "count(") && strings.Contains(lower, "logs") {
			countSQLs++
		}
	}
	require.Equal(t, 1, countSQLs,
		"5 次同参请求只应执行 1 次日志表 COUNT，实际 %d 次", countSQLs)
	for _, total := range totals {
		require.Equal(t, int64(7), total, "命中缓存的轮次 total 必须与首次一致")
	}
}
