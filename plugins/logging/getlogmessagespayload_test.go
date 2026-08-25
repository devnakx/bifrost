package logging

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockStoreWithProjection 实现 FindByIDMessagesProjection 用于快路径测试
type mockStoreWithProjection struct {
	logstore.LogStore
	calledProjection bool
	calledFindByID   bool
	projLog          *logstore.Log
	findByIDLog      *logstore.Log
	projErr          error
	findByIDErr      error
}

func (m *mockStoreWithProjection) FindByIDMessagesProjection(_ context.Context, _ string) (*logstore.Log, error) {
	m.calledProjection = true
	if m.projErr != nil {
		return nil, m.projErr
	}
	return m.projLog, nil
}

func (m *mockStoreWithProjection) FindByID(_ context.Context, _ string) (*logstore.Log, error) {
	m.calledFindByID = true
	if m.findByIDErr != nil {
		return nil, m.findByIDErr
	}
	return m.findByIDLog, nil
}

// mockStoreWithoutProjection 用于验证回退路径的语义一致性——真实 RDB 的 FindByID 全量结果经 Slice 仍与投影一致

func newTestPluginWithStore(t *testing.T, store logstore.LogStore) *LoggerPlugin {
	t.Helper()
	return &LoggerPlugin{store: store, logger: testLogger{}}
}

// (a) 快路径：mock store 实现新接口时端到端返回正确分页消息
func TestGetLogMessagesPayload_FastPath(t *testing.T) {
	ctx := context.Background()
	input := "hello fast"
	output := "world fast"
	projLog := &logstore.Log{
		ID:                 "msg-fast-1",
		Timestamp:          time.Now().UTC(),
		Provider:           "openai",
		Model:              "gpt-4o-mini",
		Object:             "chat.completion",
		Status:             "success",
		InputHistoryParsed: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &input}}},
		OutputMessageParsed: &schemas.ChatMessage{
			Role:    schemas.ChatMessageRoleAssistant,
			Content: &schemas.ChatMessageContent{ContentStr: &output},
		},
	}
	require.NoError(t, projLog.SerializeFields())

	mock := &mockStoreWithProjection{
		projLog:     projLog,
		findByIDLog: &logstore.Log{ID: "should-not-be-used"},
	}
	plugin := newTestPluginWithStore(t, mock)

	got, err := plugin.GetLogMessagesPayload(ctx, "msg-fast-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.True(t, mock.calledProjection, "快路径应委托 FindByIDMessagesProjection")
	assert.False(t, mock.calledFindByID, "快路径不应回退到 FindByID")
	assert.Equal(t, "msg-fast-1", got.ID)
	// 验证分页切片仍正确（SliceLogMessages 不改）
	msgs, total, err := logstore.SliceLogMessages(got, 0, 10)
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	assert.Len(t, msgs, 2)
}

// also via PluginLogManager wrapper
func TestPluginLogManager_GetLogMessagesPayload_FastPath(t *testing.T) {
	ctx := context.Background()
	input := "hi wrapper"
	output := "bye wrapper"
	projLog := &logstore.Log{
		ID:                 "msg-wrapper-1",
		Timestamp:          time.Now().UTC(),
		Provider:           "openai",
		Model:              "gpt-4o-mini",
		Object:             "chat.completion",
		Status:             "success",
		InputHistoryParsed: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &input}}},
		OutputMessageParsed: &schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: &output}},
	}
	require.NoError(t, projLog.SerializeFields())
	mock := &mockStoreWithProjection{projLog: projLog}
	plugin := newTestPluginWithStore(t, mock)
	mgr := plugin.GetPluginLogManager()
	got, err := mgr.GetLogMessagesPayload(ctx, "msg-wrapper-1")
	require.NoError(t, err)
	assert.True(t, mock.calledProjection)
	assert.Equal(t, "msg-wrapper-1", got.ID)
}

// (b) 回退一致性：全量 FindByID 与九列投影在消息分页语义上一致（fallback 语义验证）
func TestGetLogMessagesPayload_FallbackConsistency(t *testing.T) {
	ctx := context.Background()
	store, err := logstore.NewLogStore(ctx, &logstore.Config{
		Enabled: true,
		Type:    logstore.LogStoreTypeSQLite,
		Config:  &logstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "fallback.db")},
	}, testLogger{})
	require.NoError(t, err)
	defer store.Close(ctx)

	input := "hello fallback"
	output := "world fallback"
	fullLog := &logstore.Log{
		ID:                 "msg-fallback-1",
		Timestamp:          time.Now().UTC(),
		Provider:           "openai",
		Model:              "gpt-4o-mini",
		Object:             "chat.completion",
		Status:             "success",
		InputHistoryParsed: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &input}}},
		OutputMessageParsed: &schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: &output}},
		RawRequest:         `{"model":"gpt-4"}`,
	}
	require.NoError(t, fullLog.SerializeFields())
	require.NoError(t, store.Create(ctx, fullLog))

	// 快路径
	proj, err := store.FindByIDMessagesProjection(ctx, "msg-fallback-1")
	require.NoError(t, err)
	msgsProj, totalProj, err := logstore.SliceLogMessages(proj, 0, 10)
	require.NoError(t, err)

	// 回退路径（全量）
	full, err := store.FindByID(ctx, "msg-fallback-1")
	require.NoError(t, err)
	msgsFull, totalFull, err := logstore.SliceLogMessages(full, 0, 10)
	require.NoError(t, err)

	assert.Equal(t, totalProj, totalFull, "投影与全量分页 total 应一致")
	assert.Equal(t, len(msgsProj), len(msgsFull), "投影与全量分页长度应一致")
	// 内容一致性（消息体相同）
	for i := range msgsProj {
		assert.Equal(t, string(msgsFull[i]), string(msgsProj[i]))
	}
}
// (a) 端到端：真实 sqlite 九列投影 + hybrid + handler 路径（复用现有 fixture思路）
func TestGetLogMessagesPayload_E2E_RealStore(t *testing.T) {
	ctx := context.Background()
	store, err := logstore.NewLogStore(ctx, &logstore.Config{
		Enabled: true,
		Type:    logstore.LogStoreTypeSQLite,
		Config:  &logstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "e2e.db")},
	}, testLogger{})
	require.NoError(t, err)
	defer store.Close(ctx)
	plugin, err := Init(ctx, &Config{}, testLogger{}, store, nil, nil, nil)
	require.NoError(t, err)
	defer plugin.Cleanup()

	var chatMsgs []schemas.ChatMessage
	for i := 0; i < 5; i++ {
		c := "e2e-msg-" + string(rune('0'+i))
		cc := c
		chatMsgs = append(chatMsgs, schemas.ChatMessage{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &cc}})
	}
	outStr := "final"
	entry := &logstore.Log{
		ID:                  "e2e-msg-1",
		Timestamp:           time.Now().UTC(),
		Provider:            "openai",
		Model:               "gpt-4o-mini",
		Object:              "chat.completion",
		Status:              "success",
		InputHistoryParsed:  chatMsgs,
		OutputMessageParsed: &schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: &outStr}},
		RawRequest:          `{"big":"payload"}`,
		RawResponse:         `{"big":"response"}`,
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, store.Create(ctx, entry))

	mgr := plugin.GetPluginLogManager()
	got, err := mgr.GetLogMessagesPayload(ctx, "e2e-msg-1")
	require.NoError(t, err)
	msgs, total, err := logstore.SliceLogMessages(got, 0, 20)
	require.NoError(t, err)
	assert.Equal(t, 6, total)
	assert.Len(t, msgs, 6)
}
