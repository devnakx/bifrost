package logstore

import (
	"context"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newApplyRootsOnlyStore 构造覆盖 todo 1 四场景的最小内存库
// 种子：A(root)/B(parent=A)/C(parent=不存在)/D(self-ref)，时间递增 1s
func newApplyRootsOnlyStore(t *testing.T) (*RDBLogStore, time.Time) {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&Log{}))

	now := time.Now().UTC().Truncate(time.Millisecond)
	strPtr := func(v string) *string { return &v }

	seed := []Log{
		// A 顶层 root
		{ID: "a-root", Timestamp: now, Status: "success", Provider: "openai"},
		// B 指向 A 的子行
		{ID: "b-child", Timestamp: now.Add(time.Second), Status: "success", Provider: "openai", ParentRequestID: strPtr("a-root")},
		// C 指向不存在 id 的孤儿，自身即 root
		{ID: "c-orphan", Timestamp: now.Add(2 * time.Second), Status: "success", Provider: "openai", ParentRequestID: strPtr("nonexistent-xyz")},
		// D 自引用，parent_request_id = id，需保持顶层可见
		{ID: "d-self", Timestamp: now.Add(3 * time.Second), Status: "success", Provider: "openai", ParentRequestID: strPtr("d-self")},
	}
	for i := range seed {
		require.NoError(t, db.Create(&seed[i]).Error)
	}

	return &RDBLogStore{db: db, logger: bifrost.NewDefaultLogger(schemas.LogLevelInfo)}, now
}

// TestApplyRootsOnly 覆盖 todo 1 要求的四场景，SQLite 内存库
// (a) 无时间窗：A/C 保留，B 被隐藏
// (b) 时间窗同时含 A、B 时 B 被排除
// (c) 时间窗含 B 不含 A 时 B 提升为 root
// (d) 自引用行保持顶层可见
func TestApplyRootsOnly(t *testing.T) {
	s, now := newApplyRootsOnlyStore(t)
	ctx := context.Background()

	t.Run("a_orphan_and_root_visible_child_hidden", func(t *testing.T) {
		// (a) 父子行 A/B/C，roots_only 返回 [A, C, D]，B 被隐藏
		result, err := s.SearchLogs(ctx, SearchFilters{RootsOnly: true}, PaginationOptions{Limit: 50})
		require.NoError(t, err)
		ids := logIDs(result.Logs)
		require.ElementsMatch(t, []string{"a-root", "c-orphan", "d-self"}, ids)
		require.EqualValues(t, 3, result.Pagination.TotalCount)
	})

	t.Run("b_time_window_containing_both_hides_child", func(t *testing.T) {
		// (b) 时间窗同时含 A、B 时 B 被排除
		start := now.Add(-time.Millisecond)
		end := now.Add(1500 * time.Millisecond)
		result, err := s.SearchLogs(ctx, SearchFilters{RootsOnly: true, StartTime: &start, EndTime: &end}, PaginationOptions{Limit: 50})
		require.NoError(t, err)
		ids := logIDs(result.Logs)
		// 窗口内只有 A、B，B 指向 A 且 A 在窗内，故 B 被隐藏，仅 A 可见
		require.ElementsMatch(t, []string{"a-root"}, ids)
		require.EqualValues(t, 1, result.Pagination.TotalCount)
	})

	t.Run("c_child_promoted_when_parent_outside_window", func(t *testing.T) {
		// (c) 时间窗含 B 不含 A 时 B 作为 root 出现，父在窗外则子提升为 root
		// 钉死物化与相关子查询两分支共享的关键语义
		start := now.Add(500 * time.Millisecond)
		end := now.Add(1500 * time.Millisecond)
		result, err := s.SearchLogs(ctx, SearchFilters{RootsOnly: true, StartTime: &start, EndTime: &end}, PaginationOptions{Limit: 50})
		require.NoError(t, err)
		ids := logIDs(result.Logs)
		require.ElementsMatch(t, []string{"b-child"}, ids)
		require.EqualValues(t, 1, result.Pagination.TotalCount)
	})

	t.Run("d_self_ref_stays_visible", func(t *testing.T) {
		// (d) parent_request_id 等于自身 id 的自引用行保持顶层可见
		result, err := s.SearchLogs(ctx, SearchFilters{RootsOnly: true}, PaginationOptions{Limit: 50})
		require.NoError(t, err)
		require.Contains(t, logIDs(result.Logs), "d-self")
	})
}
