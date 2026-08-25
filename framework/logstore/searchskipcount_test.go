package logstore

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedSkipCountStore 写入 N 行，时间递增，供 TotalCount 对比
func seedSkipCountStore(t *testing.T, n int) *RDBLogStore {
	t.Helper()
	store := newProjectionTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	for i := 0; i < n; i++ {
		entry := &Log{
			ID:        "skipcount-" + time.Now().UTC().Format("150405.000000000") + "-" + string(rune('a'+i)),
			Timestamp: now.Add(time.Duration(i) * time.Second),
			Status:    "success",
			Provider:  "openai",
			Model:     "gpt-4o-mini",
		}
		// 使用确定性 ID
		entry.ID = "skip-id-" + string(rune('a'+i)) + "-" + t.Name()
		require.NoError(t, store.Create(ctx, entry))
	}
	return store
}

func TestSearchLogsSkipCount(t *testing.T) {
	store := seedSkipCountStore(t, 5)
	ctx := context.Background()

	t.Run("SkipCount_false 返回真实计数", func(t *testing.T) {
		result, err := store.SearchLogs(ctx, SearchFilters{}, PaginationOptions{Limit: 10})
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.EqualValues(t, 5, result.Pagination.TotalCount)
		assert.EqualValues(t, 5, result.Stats.TotalRequests)
		assert.Len(t, result.Logs, 5)
		assert.True(t, result.HasLogs)
	})

	t.Run("SkipCount_true 返回零值计数但数据页一致", func(t *testing.T) {
		base, err := store.SearchLogs(ctx, SearchFilters{}, PaginationOptions{Limit: 10})
		require.NoError(t, err)
		skipped, err := store.SearchLogs(ctx, SearchFilters{}, PaginationOptions{Limit: 10, SkipCount: true})
		require.NoError(t, err)

		// 计数旁路：零值
		assert.EqualValues(t, 0, skipped.Pagination.TotalCount)
		assert.EqualValues(t, 0, skipped.Stats.TotalRequests)
		// HasLogs 不受影响
		assert.Equal(t, base.HasLogs, skipped.HasLogs)
		assert.True(t, skipped.HasLogs)

		// 数据页行数与内容与未跳过完全一致
		require.Equal(t, len(base.Logs), len(skipped.Logs))
		baseIDs := make([]string, len(base.Logs))
		skipIDs := make([]string, len(skipped.Logs))
		for i := range base.Logs {
			baseIDs[i] = base.Logs[i].ID
			skipIDs[i] = skipped.Logs[i].ID
		}
		assert.Equal(t, baseIDs, skipIDs)
		// 分页回显：Limit 保持，Offset 保持，SkipCount 本身不泄漏但值传递正确
		assert.Equal(t, base.Pagination.Limit, skipped.Pagination.Limit)
	})

	t.Run("SkipCount_true 分页偏移仍生效", func(t *testing.T) {
		base, err := store.SearchLogs(ctx, SearchFilters{}, PaginationOptions{Limit: 2, Offset: 1})
		require.NoError(t, err)
		skipped, err := store.SearchLogs(ctx, SearchFilters{}, PaginationOptions{Limit: 2, Offset: 1, SkipCount: true})
		require.NoError(t, err)
		assert.EqualValues(t, 0, skipped.Pagination.TotalCount)
		require.Len(t, skipped.Logs, 2)
		require.Len(t, base.Logs, 2)
		assert.Equal(t, base.Logs[0].ID, skipped.Logs[0].ID)
		assert.Equal(t, base.Logs[1].ID, skipped.Logs[1].ID)
	})

	t.Run("SkipCount_true 空结果 HasLogs 兜底不变", func(t *testing.T) {
		// 用不存在的 provider 过滤使数据页为空，但库中仍有数据，HasLogs 应为 true
		result, err := store.SearchLogs(ctx, SearchFilters{Providers: []string{"__nonexistent__"}}, PaginationOptions{Limit: 10, SkipCount: true})
		require.NoError(t, err)
		assert.EqualValues(t, 0, result.Pagination.TotalCount)
		assert.EqualValues(t, 0, result.Stats.TotalRequests)
		assert.Empty(t, result.Logs)
		assert.True(t, result.HasLogs, "SkipCount 不得影响 HasLogs 兜底")
	})

	t.Run("JSON 不泄漏 SkipCount", func(t *testing.T) {
		result, err := store.SearchLogs(ctx, SearchFilters{}, PaginationOptions{Limit: 2, SkipCount: true})
		require.NoError(t, err)
		raw, err := json.Marshal(result)
		require.NoError(t, err)
		var m map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(raw, &m))
		var pag map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(m["pagination"], &pag))
		for k := range pag {
			assert.NotEqual(t, "skip_count", strings.ToLower(k))
			assert.NotEqual(t, "skipcount", strings.ToLower(k))
		}
		for k := range m {
			assert.NotEqual(t, "skipcount", strings.ToLower(k))
		}
		pagRaw, err := json.Marshal(result.Pagination)
		require.NoError(t, err)
		var pag2 map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(pagRaw, &pag2))
		for k := range pag2 {
			assert.NotEqual(t, "skip_count", strings.ToLower(k))
			assert.NotEqual(t, "skipcount", strings.ToLower(k))
		}
		lower := strings.ToLower(string(raw))
		assert.NotContains(t, lower, `"skip_count"`)
		assert.NotContains(t, lower, `"skipcount"`)
	})

	t.Run("JSON 正常字段仍存在", func(t *testing.T) {
		result, err := store.SearchLogs(ctx, SearchFilters{}, PaginationOptions{Limit: 2})
		require.NoError(t, err)
		raw, err := json.Marshal(result)
		require.NoError(t, err)
		var m map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(raw, &m))
		_, hasPag := m["pagination"]
		assert.True(t, hasPag)
		var pag map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(m["pagination"], &pag))
		_, hasTotal := pag["total_count"]
		assert.True(t, hasTotal, "total_count 仍需序列化")
	})
}
