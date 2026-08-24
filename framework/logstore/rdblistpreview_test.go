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

// newListPreviewStore 返回种子化内存 sqlite store，覆盖 session 预览两类场景：
// agent 续跑行（输入末条为工具输出）与普通行（末条为 user）
func newListPreviewStore(t *testing.T) *RDBLogStore {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&Log{}))

	now := time.Now()
	parent := "list-preview-parent"
	seed := []Log{
		{
			// agent 续跑轮：输入末条是工具输出，输出只有 reasoning + tool call
			ID:                    "agent-turn",
			ParentRequestID:       &parent,
			Timestamp:             now,
			Object:                "responses_stream",
			Status:                "success",
			ResponsesInputHistory: `[{"type":"message","role":"user","content":"补全这个女角色的全身三视图"},{"type":"message","role":"assistant","content":"我将按同一材质生成三视图"},{"type":"custom_tool_call_output","call_id":"c1","output":"Script completed"}]`,
			ResponsesOutput:       `[{"type":"reasoning"},{"type":"custom_tool_call","name":"image_gen"}]`,
		},
		{
			// 普通行：输入末条即 user 消息，预览行为保持不变
			ID:                    "plain-turn",
			ParentRequestID:       &parent,
			Timestamp:             now.Add(-time.Second),
			Object:                "responses_stream",
			Status:                "success",
			ResponsesInputHistory: `[{"type":"message","role":"user","content":"你好"}]`,
			ResponsesOutput:       `[{"type":"message","role":"assistant","content":"你好，有什么可以帮你"}]`,
		},
	}
	for i := range seed {
		require.NoError(t, db.Create(&seed[i]).Error)
	}

	return &RDBLogStore{db: db, logger: bifrost.NewDefaultLogger(schemas.LogLevelInfo)}
}

// session 行的输入预览应取最后一条人类消息而非工具输出，
// 保证无 assistant 输出的 agent 行仍能回退展示人类消息；
// responses_output 在 session 投影保持全量透传（与收敛前行为一致）
func TestSessionLogsPreviewSelectsLastHumanMessage(t *testing.T) {
	s := newListPreviewStore(t)
	ctx := context.Background()

	session, err := s.GetSessionLogs(ctx, "list-preview-parent", PaginationOptions{Limit: 10})
	require.NoError(t, err)
	require.Len(t, session.Logs, 2)

	byID := map[string]Log{}
	for _, l := range session.Logs {
		byID[l.ID] = l
	}

	agent := byID["agent-turn"]
	require.Len(t, agent.ResponsesInputHistoryParsed, 1, "输入预览应只保留最后一条人类消息")
	require.NotNil(t, agent.ResponsesInputHistoryParsed[0].Role)
	require.Equal(t, schemas.ResponsesInputMessageRoleAssistant, *agent.ResponsesInputHistoryParsed[0].Role)
	require.Len(t, agent.ResponsesOutputParsed, 2, "session 行 responses_output 全量透传")

	plain := byID["plain-turn"]
	require.Len(t, plain.ResponsesInputHistoryParsed, 1)
	require.NotNil(t, plain.ResponsesInputHistoryParsed[0].Role)
	require.Equal(t, schemas.ResponsesInputMessageRoleUser, *plain.ResponsesInputHistoryParsed[0].Role)
	require.Len(t, plain.ResponsesOutputParsed, 1, "session 行 responses_output 全量透传")
}
