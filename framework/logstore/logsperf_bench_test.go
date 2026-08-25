package logstore

import (
	"context"
	"fmt"
	"math/rand"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// logsperf_bench_test.go — 日志 API CPU 基线归因设施（Wave 0 / 准入）
//
// 目标：在任何产品代码改动前锁定热点基线，供后续 todo 8 前后对比
//
// 两档数据：5000 行（小服务器现状）与 100000 行（roots_only 护栏）
// 每行：timestamp 均匀分布 24h 窗口、随机 provider/model、10% 父子关系、
// input_history 2-4 条消息、output_message 1 条、10% 行含 500KB raw_request
//
// 归因维度（需用 pprof top 归一后人工判定）：
//  1. COUNT SQL — searchLogs 的 Count goroutine，符号：gorm Count / getCountFromMatView / applyFilters
//  2. 数据页行读取 + AfterFind 反序列化 — sqlite 读页 + Log.DeserializeFields / sonic.Unmarshal
//  3. attachChildAggregates — 仅 RootsOnly 路径，符号：attachChildAggregates / WHERE parent_request_id IN
//  4. 响应序列化 — handler 层 sonic.Marshal(SearchResult)；本 bench 不直接覆盖，需在 transports 层压测补充
//
// 运行：
//  go test ./logstore/ -bench 'BenchmarkSearchLogs|BenchmarkGetLogMessages' -benchtime 5x -run xxx -count 3 -benchmem -cpuprofile cpu.out
//  go tool pprof -top cpu.out               # flat / cumulative 归因表
//  go tool pprof -top -cum cpu.out          # 含 cumulative 便于看 COUNT vs 扫描占比
//
// 判定：若 COUNT 占比 < 30%，在归因报告中下调缓存收益预期（不推翻方案）
// 报告落盘：.omo/evidence/task-3-logs-api-cpu-optimization.txt 与 learnings.md

var (
	benchProviders = []string{"openai", "anthropic", "gemini", "groq", "bedrock"}
	benchModels    = []string{"gpt-4o-mini", "claude-3-5-sonnet", "gemini-2.0-flash", "llama-3-70b", "mistral-large"}

	// 10% 行携带 500KB raw_request，复用同一块内存避免重复分配
	benchLargeRawRequest = strings.Repeat("x", 500*1024)
)

// newBenchSQLiteStore 构造隔离的 sqlite 库（文件位于 b.TempDir，避免污染）
// 内存库语义：每个 benchmark 独占文件，测真实落盘+读页成本
func newBenchSQLiteStore(b *testing.B) *RDBLogStore {
	b.Helper()
	store, err := newSqliteLogStore(context.Background(), &SQLiteConfig{
		Path: filepath.Join(b.TempDir(), "bench.db"),
	}, testLogger{})
	if err != nil {
		b.Fatalf("newSqliteLogStore bench: %v", err)
	}
	return store
}

// seedBenchLogs 均匀写入 n 行，返回全部 id 与 24h 窗口边界
// 约定：调用方应在 b.ResetTimer 前完成，避免把建库/种子计入 bench
func seedBenchLogs(b *testing.B, store *RDBLogStore, n int) ([]string, time.Time, time.Time) {
	b.Helper()
	// 固定随机种子保证跨轮可比
	rnd := rand.New(rand.NewSource(42))
	windowStart := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Millisecond)
	windowEnd := windowStart.Add(24 * time.Hour)
	// 均匀步长 + 毫秒级抖动避免完全等间距
	interval := time.Duration(int64(24*time.Hour) / int64(n))
	ids := make([]string, 0, n)

	// 单事务批量写入，避免每批一次事务提交的 WAL 同步开销（100k 档由 74s 降至数秒）
	tx := store.db.WithContext(context.Background()).Begin()
	if tx.Error != nil {
		b.Fatalf("begin bench seed tx: %v", tx.Error)
	}
	batch := make([]*Log, 0, 50)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := tx.Create(&batch).Error; err != nil {
			b.Fatalf("bench seed tx create: %v", err)
		}
		batch = batch[:0]
	}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("bench-%d-%08d", n, i)
		ts := windowStart.Add(time.Duration(i) * interval).Add(time.Duration(rnd.Intn(900)) * time.Millisecond)
		if ts.After(windowEnd) {
			ts = windowEnd.Add(-time.Millisecond)
		}
		provider := benchProviders[rnd.Intn(len(benchProviders))]
		model := benchModels[rnd.Intn(len(benchModels))]
		// 10% 父子关系：指向已写入的随机父
		var parentID *string
		if i > 0 && rnd.Intn(10) == 0 {
			p := ids[rnd.Intn(len(ids))]
			// 避免自指
			if p != id {
				parentID = &p
			}
		}
		// input_history 2-4 条消息
		msgCount := 2 + rnd.Intn(3)
		msgs := make([]schemas.ChatMessage, 0, msgCount)
		for j := 0; j < msgCount; j++ {
			role := schemas.ChatMessageRoleUser
			if j%2 == 1 {
				role = schemas.ChatMessageRoleAssistant
			}
			content := fmt.Sprintf("bench input %d-%d provider=%s model=%s seq=%d", n, i, provider, model, j)
			cs := content
			msgs = append(msgs, schemas.ChatMessage{
				Role: role,
				Content: &schemas.ChatMessageContent{
					ContentStr: &cs,
				},
			})
		}
		reply := fmt.Sprintf("bench output %d-%d reply", n, i)
		latency := float64(50 + rnd.Intn(950))
		totalTokens := 50 + rnd.Intn(2000)
		costVal := float64(rnd.Intn(100)) / 1000.0

		entry := &Log{
			ID:              id,
			ParentRequestID: parentID,
			Timestamp:       ts,
			Object:          "chat.completion",
			Provider:        provider,
			Model:           model,
			Status:          "success",
			Latency:         &latency,
			TotalTokens:     totalTokens,
			Cost:            &costVal,
			InputHistoryParsed:          msgs,
			OutputMessageParsed: &schemas.ChatMessage{
				Role: schemas.ChatMessageRoleAssistant,
				Content: &schemas.ChatMessageContent{
					ContentStr: &reply,
				},
			},
		}
		// 10% 行含 500KB raw_request
		if rnd.Intn(10) == 0 {
			entry.RawRequest = benchLargeRawRequest
		}
		batch = append(batch, entry)
		ids = append(ids, id)
		if len(batch) >= 50 {
			flush()
		}
	}
	flush()
	if err := tx.Commit().Error; err != nil {
		b.Fatalf("bench seed tx commit: %v", err)
	}
	return ids, windowStart, windowEnd
}

// BenchmarkSearchLogs24h 默认形态：period 24h、limit 50、timestamp desc
// 子 bench 区分 5k / 100k 两档
func BenchmarkSearchLogs24h(b *testing.B) {
	for _, tc := range []struct {
		name string
		rows int
	}{
		{"rows_5000", 5000},
		{"rows_100000", 100000},
	} {
		b.Run(tc.name, func(b *testing.B) {
			store := newBenchSQLiteStore(b)
			_, start, end := seedBenchLogs(b, store, tc.rows)
			filters := SearchFilters{
				StartTime: &start,
				EndTime:   &end,
			}
			pagination := PaginationOptions{
				Limit:  50,
				SortBy: "timestamp",
				Order:  "desc",
			}
			// 预热一次避免首轮冷编译/缓存干扰
			if _, err := store.SearchLogs(context.Background(), filters, pagination); err != nil {
				b.Fatalf("warmup SearchLogs: %v", err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				res, err := store.SearchLogs(context.Background(), filters, pagination)
				if err != nil {
					b.Fatalf("SearchLogs: %v", err)
				}
				// 防止编译器优化掉结果
				if len(res.Logs) == 0 {
					b.Fatalf("expected logs")
				}
			}
		})
	}
}

// BenchmarkSearchLogsRootsOnly roots_only 过滤形态，触发 attachChildAggregates
func BenchmarkSearchLogsRootsOnly(b *testing.B) {
	for _, tc := range []struct {
		name string
		rows int
	}{
		{"rows_5000", 5000},
		{"rows_100000", 100000},
	} {
		b.Run(tc.name, func(b *testing.B) {
			store := newBenchSQLiteStore(b)
			_, start, end := seedBenchLogs(b, store, tc.rows)
			filters := SearchFilters{
				StartTime: &start,
				EndTime:   &end,
				RootsOnly: true,
			}
			pagination := PaginationOptions{
				Limit:  50,
				SortBy: "timestamp",
				Order:  "desc",
			}
			if _, err := store.SearchLogs(context.Background(), filters, pagination); err != nil {
				b.Fatalf("warmup SearchLogs roots: %v", err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				res, err := store.SearchLogs(context.Background(), filters, pagination)
				if err != nil {
					b.Fatalf("SearchLogs roots: %v", err)
				}
				if res == nil {
					b.Fatalf("nil result")
				}
			}
		})
	}
}

// BenchmarkGetLogMessages FindByID 全量路径 — 后续 todo 8 对比 FindByIDMessagesProjection
func BenchmarkGetLogMessages(b *testing.B) {
	for _, tc := range []struct {
		name string
		rows int
	}{
		{"rows_5000", 5000},
		{"rows_100000", 100000},
	} {
		b.Run(tc.name+"/FindByID", func(b *testing.B) {
			store := newBenchSQLiteStore(b)
			ids, _, _ := seedBenchLogs(b, store, tc.rows)
			rnd := rand.New(rand.NewSource(99))
			ctx := context.Background()
			// 预热
			if _, err := store.FindByID(ctx, ids[0]); err != nil {
				b.Fatalf("warmup FindByID: %v", err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				id := ids[rnd.Intn(len(ids))]
				row, err := store.FindByID(ctx, id)
				if err != nil {
					b.Fatalf("FindByID: %v", err)
				}
				if row == nil || row.ID == "" {
					b.Fatalf("empty row")
				}
			}
		})
		b.Run(tc.name+"/FindByIDMessagesProjection", func(b *testing.B) {
			store := newBenchSQLiteStore(b)
			ids, _, _ := seedBenchLogs(b, store, tc.rows)
			rnd := rand.New(rand.NewSource(99))
			ctx := context.Background()
			if _, err := store.FindByIDMessagesProjection(ctx, ids[0]); err != nil {
				b.Fatalf("warmup FindByIDMessagesProjection: %v", err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				id := ids[rnd.Intn(len(ids))]
				row, err := store.FindByIDMessagesProjection(ctx, id)
				if err != nil {
					b.Fatalf("FindByIDMessagesProjection: %v", err)
				}
				if row == nil || row.ID == "" {
					b.Fatalf("empty row")
				}
			}
		})
	}
}
