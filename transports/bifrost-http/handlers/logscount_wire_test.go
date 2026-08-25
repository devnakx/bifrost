package handlers

import (
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
)

// (a) TTL 边界：写入 12s + sleep 13s 后 stale miss，第二次重新 COUNT
func TestLogsCountCache_TTLBoundary_StaleMiss(t *testing.T) {
	SetLogger(&mockLogger{})
	mock := newCountMock(10)
	h := NewLoggingHandler(mock, &logsListCapRedactedKeysStub{}, &lib.Config{})
	uri := "/api/logs?period=24h"
	_, r1 := doGetLogs(t, h, uri, "", 0)
	if r1.Pagination.TotalCount != 10 {
		t.Fatalf("第一次 total 期望 10，实际 %d", r1.Pagination.TotalCount)
	}
	// 人为将该 key 的过期时间回拨到过去，模拟已过期
	h.logsCountCache.mu.Lock()
	for _, e := range h.logsCountCache.entries {
		e.expiresAt = time.Now().Add(-time.Second)
	}
	h.logsCountCache.mu.Unlock()
	_, r2 := doGetLogs(t, h, uri, "", 0)
	calls := mock.callsSnapshot()
	last := calls[len(calls)-1]
	if last.SkipCount {
		t.Fatalf("过期后应 stale miss SkipCount=false，实际 true")
	}
	if r2.Pagination.TotalCount != 10 {
		t.Fatalf("过期后重新 COUNT total 应 10，实际 %d", r2.Pagination.TotalCount)
	}
	// 过期后这次写回应使下一次命中
	_, _ = doGetLogs(t, h, uri, "", 0)
	calls = mock.callsSnapshot()
	if !calls[len(calls)-1].SkipCount {
		t.Fatalf("重写后再次命中应 SkipCount=true")
	}
}

// (b) TTL 边界 12s 内仍命中
func TestLogsCountCache_TTLBoundary_StillFresh(t *testing.T) {
	SetLogger(&mockLogger{})
	mock := newCountMock(20)
	h := NewLoggingHandler(mock, &logsListCapRedactedKeysStub{}, &lib.Config{})
	uri := "/api/logs?period=24h"
	doGetLogs(t, h, uri, "", 0)
	// 将过期推到未来 5s 仍在 TTL 内
	h.logsCountCache.mu.Lock()
	for _, e := range h.logsCountCache.entries {
		e.expiresAt = time.Now().Add(5 * time.Second)
	}
	h.logsCountCache.mu.Unlock()
	_, r := doGetLogs(t, h, uri, "", 0)
	calls := mock.callsSnapshot()
	if !calls[len(calls)-1].SkipCount {
		t.Fatalf("未来过期内应命中 SkipCount=true")
	}
	if r.Pagination.TotalCount != 20 {
		t.Fatalf("命中 total 应 20，实际 %d", r.Pagination.TotalCount)
	}
}

// (c) DELETE 后陈旧窗口：purge 清空后同窗重建，不同窗不互相污染
func TestLogsCountCache_DeleteThenStaleWindowEquivalence(t *testing.T) {
	SetLogger(&mockLogger{})
	mock := newCountMock(30)
	h := NewLoggingHandler(mock, &logsListCapRedactedKeysStub{}, &lib.Config{})
	doGetLogs(t, h, "/api/logs?period=24h", "", 0)
	doGetLogs(t, h, "/api/logs?period=7d", "", 0)
	if size := logsCountCacheSize(h); size != 2 {
		t.Fatalf("预置两窗 size 应 2，实际 %d", size)
	}
	ctx := doDeleteLogs(t, h, []string{"x"})
	if ctx.Response.StatusCode() != 200 {
		t.Fatalf("delete 期望 200，实际 %d %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
	if size := logsCountCacheSize(h); size != 0 {
		t.Fatalf("purge 后 size 应 0，实际 %d", size)
	}
	// 各窗首次重建均应 COUNT
	doGetLogs(t, h, "/api/logs?period=24h", "", 0)
	calls := mock.callsSnapshot()
	if calls[len(calls)-1].SkipCount {
		t.Fatalf("24h delete 后首次应 COUNT SkipCount=false")
	}
	doGetLogs(t, h, "/api/logs?period=7d", "", 0)
	calls = mock.callsSnapshot()
	if calls[len(calls)-1].SkipCount {
		t.Fatalf("7d delete 后首次应 COUNT SkipCount=false")
	}
	// 二次命中
	_, r1 := doGetLogs(t, h, "/api/logs?period=24h", "", 0)
	_, r2 := doGetLogs(t, h, "/api/logs?period=7d", "", 0)
	if r1.Pagination.TotalCount != 30 || r2.Pagination.TotalCount != 30 {
		t.Fatalf("重建后 total 应 30，实际 %d %d", r1.Pagination.TotalCount, r2.Pagination.TotalCount)
	}
}

// (d) 并发 DELETE 与并发 getLogs 交错不死锁，且最终可继续服务
func TestLogsCountCache_ConcurrentDeleteAndGetLogs_NoDeadlock(t *testing.T) {
	SetLogger(&mockLogger{})
	mock := newCountMock(40)
	h := NewLoggingHandler(mock, &logsListCapRedactedKeysStub{}, &lib.Config{})
	// 预热一窗
	doGetLogs(t, h, "/api/logs?period=24h", "", 0)
	done := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-done:
				return
			default:
			}
			doGetLogs(t, h, "/api/logs?period=24h", "", 0)
		}()
	}
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-done:
				return
			default:
			}
			doDeleteLogs(t, h, []string{"y"})
		}()
	}
	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		close(done)
		t.Fatal("并发 DELETE/getLogs 5s 未完成，可能死锁")
	}
	// 结束后仍可正常 COUNT
	_, r := doGetLogs(t, h, "/api/logs?period=24h", "", 0)
	if r.Pagination.TotalCount != 40 {
		t.Fatalf("并发后 total 应 40，实际 %d", r.Pagination.TotalCount)
	}
}

// (e) wire 契约：非 content_search 的 total 通过缓存与直通 COUNT 一致，has_more 由数据页决定不由缓存污染
func TestLogsCountCache_WireTotalEquivalenceAfterPurge(t *testing.T) {
	SetLogger(&mockLogger{})
	mock := newCountMock(50)
	h := NewLoggingHandler(mock, &logsListCapRedactedKeysStub{}, &lib.Config{})
	_, r1 := doGetLogs(t, h, "/api/logs?period=24h&limit=10&offset=0", "", 0)
	_, r2 := doGetLogs(t, h, "/api/logs?period=24h&limit=10&offset=20", "", 0)
	if r1.Pagination.TotalCount != r2.Pagination.TotalCount || r1.Pagination.TotalCount != 50 {
		t.Fatalf("缓存命中 total 应一致 50，实际 %d vs %d", r1.Pagination.TotalCount, r2.Pagination.TotalCount)
	}
	doDeleteLogs(t, h, []string{"z"})
	_, r3 := doGetLogs(t, h, "/api/logs?period=24h&limit=10&offset=20", "", 0)
	if r3.Pagination.TotalCount != 50 {
		t.Fatalf("purge 后重建 total 应 50，实际 %d", r3.Pagination.TotalCount)
	}
}
