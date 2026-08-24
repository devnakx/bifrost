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

// GET /api/logs 列表行 input 预览有界的 handler 层钉子：超大单条输入消息经
// 完整 fasthttp 请求 → getLogs → 真实 sqlite SearchLogs 链路后，对应日志行的
// input_history 只携带被选中的单条消息且序列化长度有界，整页响应不超 1MB
//
// 本测试是 provider-harness 回归用例 logs-input-preview-cap 的 handler 层等价
// 断言：harness 用例因环境无付费 provider 凭据无法发起 ≥1MB 真实落库请求，
// wire 级红绿由本测试在真实存储上完成

const logsListCapElementBytes = 16384
const logsListCapPageBytes = 1 << 20

// logsListCapRedactedKeysStub 空实现：本用例行不含 key/vk/routing 归属，脱敏查询不应被触发
type logsListCapRedactedKeysStub struct{}

func (s *logsListCapRedactedKeysStub) GetAllRedactedKeys(ctx context.Context, ids []string) []schemas.Key {
	return nil
}
func (s *logsListCapRedactedKeysStub) GetAllRedactedVirtualKeys(ctx context.Context, ids []string) []configstoretables.TableVirtualKey {
	return nil
}
func (s *logsListCapRedactedKeysStub) GetAllRedactedRoutingRules(ctx context.Context, ids []string) []configstoretables.TableRoutingRule {
	return nil
}

// newLogsListCapFixture 构建真实 sqlite logstore + 真实 logging 插件 LogManager 的 handler 与存储
func newLogsListCapFixture(t *testing.T) (*LoggingHandler, logstore.LogStore) {
	t.Helper()
	ctx := context.Background()

	store, err := logstore.NewLogStore(ctx, &logstore.Config{
		Enabled: true,
		Type:    logstore.LogStoreTypeSQLite,
		Config:  &logstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "logs.db")},
	}, testLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close(ctx) })

	plugin, err := loggingplugin.Init(ctx, &loggingplugin.Config{}, testLogger{}, store, nil, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = plugin.Cleanup() })

	handler := NewLoggingHandler(plugin.GetPluginLogManager(), &logsListCapRedactedKeysStub{}, &lib.Config{})
	return handler, store
}

// TestGetLogsListRowsBoundedInputPreview 超大单条输入消息的日志行在 /api/logs 列表中预览有界
func TestGetLogsListRowsBoundedInputPreview(t *testing.T) {
	h, store := newLogsListCapFixture(t)
	ctx := context.Background()

	// 约 17MB 对齐生产实测的单条巨型输入消息规模
	big := strings.Repeat("x", 17<<20)
	entry := &logstore.Log{
		ID:        "logs-list-cap-1",
		Timestamp: time.Now().UTC(),
		Object:    "chat.completion",
		Provider:  "openai",
		Model:     "gpt-4o-mini",
		Status:    "success",
		InputHistoryParsed: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: &big},
		}},
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, store.Create(ctx, entry))

	var req fasthttp.Request
	req.SetRequestURI("/api/logs?limit=5&sort_by=timestamp&order=desc")
	reqCtx := &fasthttp.RequestCtx{}
	reqCtx.Init(&req, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}, nil)

	h.getLogs(reqCtx)

	require.Equal(t, fasthttp.StatusOK, reqCtx.Response.StatusCode(), "GET /api/logs 应返回 200：%s", string(reqCtx.Response.Body()))
	body := reqCtx.Response.Body()
	assert.LessOrEqual(t, len(body), logsListCapPageBytes, "列表响应总大小须不超过 1MB")

	var resp struct {
		Logs []struct {
			ID           string            `json:"id"`
			InputHistory []json.RawMessage `json:"input_history"`
		} `json:"logs"`
	}
	require.NoError(t, sonic.Unmarshal(body, &resp))
	var row *struct {
		ID           string            `json:"id"`
		InputHistory []json.RawMessage `json:"input_history"`
	}
	for i := range resp.Logs {
		if resp.Logs[i].ID == "logs-list-cap-1" {
			row = &resp.Logs[i]
			break
		}
	}
	require.NotNil(t, row, "响应中应包含本次落库的日志行")
	require.Len(t, row.InputHistory, 1, "列表行 input_history 只应携带被选中的末条消息")
	assert.LessOrEqual(t, len(row.InputHistory[0]), logsListCapElementBytes, "单元素序列化长度须不超过 16384 字节")
}
