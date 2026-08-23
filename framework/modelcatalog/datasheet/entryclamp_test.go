package datasheet

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestConvertEntryToTablePricing_ClampsNegativeCostsToZero(t *testing.T) {
	entry := Entry{
		Provider: "openai",
		Mode:     "chat",
		Options: Options{
			InputCostPerToken: bifrost.Ptr(-1.0),
		},
	}
	table := convertEntryToTablePricing("openai/gpt-4o", entry)
	if table.InputCostPerToken == nil || *table.InputCostPerToken != 0 {
		t.Fatalf("expected negative input cost to clamp to 0, got %#v", table.InputCostPerToken)
	}
}

func TestConvertEntryToTablePricing_ZeroCostStaysZero(t *testing.T) {
	entry := Entry{
		Provider: "openai",
		Mode:     "chat",
		Options: Options{
			InputCostPerToken: bifrost.Ptr(0.0),
		},
	}
	table := convertEntryToTablePricing("openai/gpt-4o", entry)
	if table.InputCostPerToken == nil || *table.InputCostPerToken != 0 {
		t.Fatalf("expected zero input cost to stay 0, got %#v", table.InputCostPerToken)
	}
}

func TestConvertEntryToTablePricing_PositiveCostUnchanged(t *testing.T) {
	entry := Entry{
		Provider: "openai",
		Mode:     "chat",
		Options: Options{
			InputCostPerToken: bifrost.Ptr(0.0001),
		},
	}
	table := convertEntryToTablePricing("openai/gpt-4o", entry)
	if table.InputCostPerToken == nil || *table.InputCostPerToken != 0.0001 {
		t.Fatalf("expected positive input cost unchanged, got %#v", table.InputCostPerToken)
	}
}

func TestConvertEntryToTablePricing_NilCostStaysNil(t *testing.T) {
	entry := Entry{
		Provider: "openai",
		Mode:     "chat",
		Options: Options{
			InputCostPerToken: nil,
		},
	}
	table := convertEntryToTablePricing("openai/gpt-4o", entry)
	if table.InputCostPerToken != nil {
		t.Fatalf("expected nil input cost to stay nil, got %#v", table.InputCostPerToken)
	}
}

func TestConvertEntryToTablePricing_ClampsMixedFieldsIndependently(t *testing.T) {
	entry := Entry{
		Provider: "openai",
		Mode:     "chat",
		Options: Options{
			InputCostPerToken:           bifrost.Ptr(-1.0),
			OutputCostPerToken:          bifrost.Ptr(2.5),
			CacheCreationInputTokenCost: bifrost.Ptr(-0.5),
		},
	}
	table := convertEntryToTablePricing("openai/gpt-4o", entry)
	if table.InputCostPerToken == nil || *table.InputCostPerToken != 0 {
		t.Fatalf("expected negative input cost to clamp to 0, got %#v", table.InputCostPerToken)
	}
	if table.OutputCostPerToken == nil || *table.OutputCostPerToken != 2.5 {
		t.Fatalf("expected positive output cost unchanged, got %#v", table.OutputCostPerToken)
	}
	if table.CacheCreationInputTokenCost == nil || *table.CacheCreationInputTokenCost != 0 {
		t.Fatalf("expected negative cache write cost to clamp to 0, got %#v", table.CacheCreationInputTokenCost)
	}
}

func TestConvertEntryToTablePricing_NonPricingFieldsUntouched(t *testing.T) {
	entry := Entry{
		Provider:       "openai",
		Mode:           "chat",
		ContextLength:  bifrost.Ptr(-1),
		MaxInputTokens: bifrost.Ptr(-2),
	}
	table := convertEntryToTablePricing("openai/gpt-4o", entry)
	// *int 字段不属于价格字段，不应被归一化
	if table.ContextLength == nil || *table.ContextLength != -1 {
		t.Fatalf("expected context length to pass through untouched, got %#v", table.ContextLength)
	}
	if table.MaxInputTokens == nil || *table.MaxInputTokens != -2 {
		t.Fatalf("expected max input tokens to pass through untouched, got %#v", table.MaxInputTokens)
	}
}

func TestConvertEntryToTablePricing_ClampDoesNotMutateEntryPointers(t *testing.T) {
	entry := Entry{
		Provider: "openai",
		Mode:     "chat",
		Options: Options{
			InputCostPerToken: bifrost.Ptr(-1.0),
		},
	}
	table := convertEntryToTablePricing("openai/gpt-4o", entry)
	// 入参 Entry 的指针不被原地修改，避免调用方持有的引用被意外篡改
	if entry.InputCostPerToken == nil || *entry.InputCostPerToken != -1.0 {
		t.Fatalf("expected source entry pointer untouched, got %#v", entry.InputCostPerToken)
	}
	if table.InputCostPerToken == entry.InputCostPerToken {
		t.Fatal("expected clamped field to use a fresh pointer, not alias the source")
	}
}

// TestLoadFromLocalFiles_ClampsNegativePricing 端到端验证归一化覆盖完整同步链路
// 复现线上场景：datasheet 文件携带 *_cost_per_token: -1 哨兵值
// 期望 LoadFromURLIntoMemory 之后任何读取路径拿到的都是 0 而非负数
func TestLoadFromLocalFiles_ClampsNegativePricing(t *testing.T) {
	// 构造含负哨兵值的临时 datasheet，模拟 default_config.content.json 的异常数据
	datasheet := `{
		"aihubmix/auto": {
			"provider": "AIHubMix",
			"mode": "chat",
			"max_input_tokens": 2000000,
			"input_cost_per_token": -1,
			"output_cost_per_token": -1,
			"cache_read_input_token_cost": -0.5,
			"cache_creation_input_token_cost": 0.25
		}
	}`
	dir := t.TempDir()
	pricingFile := filepath.Join(dir, "pricing.json")
	if err := os.WriteFile(pricingFile, []byte(datasheet), 0o600); err != nil {
		t.Fatalf("failed to write temp datasheet: %v", err)
	}

	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	store := New(nil, logger, Config{URL: "file://" + pricingFile})
	if err := store.LoadFromURLIntoMemory(context.Background()); err != nil {
		t.Fatalf("LoadFromURLIntoMemory failed: %v", err)
	}

	entry := store.GetCapabilityEntry("auto", "AIHubMix")
	if entry == nil {
		t.Fatal("expected capability entry for aihubmix/auto after load")
	}
	if entry.InputCostPerToken == nil || *entry.InputCostPerToken != 0 {
		t.Fatalf("expected negative input cost normalized to 0, got %#v", entry.InputCostPerToken)
	}
	if entry.OutputCostPerToken == nil || *entry.OutputCostPerToken != 0 {
		t.Fatalf("expected negative output cost normalized to 0, got %#v", entry.OutputCostPerToken)
	}
	if entry.CacheReadInputTokenCost == nil || *entry.CacheReadInputTokenCost != 0 {
		t.Fatalf("expected negative cache read cost normalized to 0, got %#v", entry.CacheReadInputTokenCost)
	}
	if entry.CacheCreationInputTokenCost == nil || *entry.CacheCreationInputTokenCost != 0.25 {
		t.Fatalf("expected positive cache write cost preserved, got %#v", entry.CacheCreationInputTokenCost)
	}
}
