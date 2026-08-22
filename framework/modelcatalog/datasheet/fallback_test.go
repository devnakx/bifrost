package datasheet

import (
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedRow 构造与 makeKey 三要素一致的定价行
func seedRow(model, provider, mode string, input, output float64) configstoreTables.TableModelPricing {
	return configstoreTables.TableModelPricing{
		Model:              model,
		Provider:           provider,
		Mode:               mode,
		InputCostPerToken:  bifrost.Ptr(input),
		OutputCostPerToken: bifrost.Ptr(output),
	}
}

// 自定义 provider 应命中官方 vendor 的行而非字典序首行
func TestCrossProviderFallback_PrefersOfficialVendor(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("deepseek-v4-flash", "deepseek", "chat"): seedRow("deepseek-v4-flash", "deepseek", "chat", 2e-7, 4e-7),
		makeKey("deepseek-v4-flash", "azure", "chat"):    seedRow("deepseek-v4-flash", "azure", "chat", 1.74e-6, 3.48e-6),
	})

	row, ok := s.getBasePricing("deepseek-v4-flash", "aihubmix", schemas.ChatCompletionRequest)
	require.True(t, ok)
	require.NotNil(t, row.InputCostPerToken)
	assert.InDelta(t, 2e-7, *row.InputCostPerToken, 1e-12)

	entry := s.GetCapabilityEntry("deepseek-v4-flash", "aihubmix")
	require.NotNil(t, entry)
	require.NotNil(t, entry.InputCostPerToken)
	assert.InDelta(t, 2e-7, *entry.InputCostPerToken, 1e-12)

	pricingEntry := s.GetPricingEntryForModel("deepseek-v4-flash", "aihubmix")
	require.NotNil(t, pricingEntry)
	require.NotNil(t, pricingEntry.InputCostPerToken)
	assert.InDelta(t, 2e-7, *pricingEntry.InputCostPerToken, 1e-12)
}

// 推断不出 vendor 时按 key 字典序取首行保证确定性
func TestCrossProviderFallback_LexicographicWhenNoVendorInferred(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("nova-canvas", "workers-ai", "chat"): seedRow("nova-canvas", "workers-ai", "chat", 1, 1),
		makeKey("nova-canvas", "azure", "chat"):      seedRow("nova-canvas", "azure", "chat", 2, 2),
	})

	row, ok := s.getBasePricing("nova-canvas", "aihubmix", schemas.ChatCompletionRequest)
	require.True(t, ok)
	require.NotNil(t, row.InputCostPerToken)
	assert.InDelta(t, 2, *row.InputCostPerToken, 0)
}

// 候选行跨 mode 时优先请求的 mode，缺请求 mode 才回退相邻 mode
func TestCrossProviderFallback_ModePreference(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("weird-model", "azure", "responses"):    seedRow("weird-model", "azure", "responses", 1, 1),
		makeKey("weird-model", "dashscope", "chat"):     seedRow("weird-model", "dashscope", "chat", 2, 2),
	})

	row, ok := s.getBasePricing("weird-model", "aihubmix", schemas.ChatCompletionRequest)
	require.True(t, ok)
	require.NotNil(t, row.InputCostPerToken)
	assert.InDelta(t, 2, *row.InputCostPerToken, 0)

	s2 := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("weird-model", "azure", "responses"): seedRow("weird-model", "azure", "responses", 1, 1),
	})
	row2, ok2 := s2.getBasePricing("weird-model", "aihubmix", schemas.ChatCompletionRequest)
	require.True(t, ok2)
	require.NotNil(t, row2.InputCostPerToken)
	assert.InDelta(t, 1, *row2.InputCostPerToken, 0)
}

// datasheet 有行的标准 provider 缺模型时不得继承别家价格
func TestCrossProviderFallback_KnownProviderNotApplied(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("deepseek-v4-flash", "deepseek", "chat"): seedRow("deepseek-v4-flash", "deepseek", "chat", 2e-7, 4e-7),
		makeKey("gpt-4o", "openai", "chat"):              seedRow("gpt-4o", "openai", "chat", 3, 4),
	})

	_, ok := s.getBasePricing("deepseek-v4-flash", "openai", schemas.ChatCompletionRequest)
	assert.False(t, ok)
	assert.Nil(t, s.GetCapabilityEntry("deepseek-v4-flash", "openai"))
	assert.Nil(t, s.GetPricingEntryForModel("deepseek-v4-flash", "openai"))
}

// 带 provider 前缀的 wire 模型名经 base model 家族命中兜底行
func TestCrossProviderFallback_BaseModelFamily(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("deepseek-v4-flash", "deepseek", "chat"): seedRow("deepseek-v4-flash", "deepseek", "chat", 2e-7, 4e-7),
	})
	s.baseModelIndex["deepseek-v4-flash-0423"] = "deepseek-v4-flash"

	row, ok := s.getBasePricing("deepseek/deepseek-v4-flash-0423", "ofoxai", schemas.ChatCompletionRequest)
	require.True(t, ok)
	require.NotNil(t, row.InputCostPerToken)
	assert.InDelta(t, 2e-7, *row.InputCostPerToken, 1e-12)
}

// 端到端：自定义 provider 的响应按兜底价格算出非零 cost
func TestCalculateCost_CustomProviderUsesFallback(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("deepseek-v4-flash", "deepseek", "chat"): seedRow("deepseek-v4-flash", "deepseek", "chat", 2e-7, 4e-7),
	})

	usage := &schemas.BifrostLLMUsage{PromptTokens: 1000, CompletionTokens: 1000, TotalTokens: 2000}
	resp := makeChatResponse("aihubmix", "deepseek-v4-flash", usage)
	cost := s.CalculateCost(resp, &LookupScopes{Provider: "aihubmix"})
	assert.InDelta(t, 1000*2e-7+1000*4e-7, cost, 1e-9)
}

// 兜底基价之上 override 仍然优先
func TestCrossProviderFallback_OverrideStillWins(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("deepseek-v4-flash", "deepseek", "chat"): seedRow("deepseek-v4-flash", "deepseek", "chat", 2e-7, 4e-7),
	})
	require.NoError(t, s.SetOverrides([]configstoreTables.TablePricingOverride{
		{
			ID:               "global-deepseek",
			ScopeKind:        string(ScopeKindGlobal),
			MatchType:        string(MatchTypeExact),
			Pattern:          "deepseek-v4-flash",
			RequestTypes:     []schemas.RequestType{schemas.ChatCompletionRequest},
			PricingPatchJSON: `{"input_cost_per_token":1}`,
		},
	}))

	pricing := s.resolvePricing(schemas.RoutingInfo{Provider: "aihubmix", Model: "deepseek-v4-flash"}, schemas.ChatCompletionRequest, LookupScopes{Provider: "aihubmix"})
	require.NotNil(t, pricing)
	require.NotNil(t, pricing.InputCostPerToken)
	assert.InDelta(t, 1, *pricing.InputCostPerToken, 0)
	// 未被 patch 的 output 仍取兜底基价
	require.NotNil(t, pricing.OutputCostPerToken)
	assert.InDelta(t, 4e-7, *pricing.OutputCostPerToken, 1e-12)
}
