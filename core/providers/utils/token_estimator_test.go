package utils

import (
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestEstimateTokensByModelUsesProviderWeights(t *testing.T) {
	tests := []struct {
		name  string
		model string
		text  string
		want  int
	}{
		{name: "openai tokenizer word", model: "gpt-4o", text: "hello", want: 1},
		{name: "claude cjk", model: "claude-3-7-sonnet", text: "你好", want: 3},
		{name: "gemini emoji", model: "gemini-2.5-pro", text: "😀", want: 2},
		{name: "openai tokenizer number", model: "gpt-4o", text: "123", want: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EstimateTokensByModel(tt.model, tt.text); got != tt.want {
				t.Fatalf("EstimateTokensByModel(%q, %q) = %d, want %d", tt.model, tt.text, got, tt.want)
			}
		})
	}
}

func TestStreamTokenEstimatorPreservesWordBoundaryAcrossChunks(t *testing.T) {
	whole := EstimateTokensByModel("claude-3-7-sonnet", "hello world")
	estimator := newStreamTokenEstimator("claude-3-7-sonnet")
	estimator.Add("hel")
	estimator.Add("lo world")

	if got := estimator.Tokens(); got != whole {
		t.Fatalf("stream estimate = %d, whole estimate = %d", got, whole)
	}
}

func TestEstimateStreamPromptTokensIgnoresNonTextStreams(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	request := &schemas.BifrostRequest{
		RequestType: schemas.ImageGenerationStreamRequest,
	}

	EstimateStreamPromptTokens(ctx, request)

	if got := ctx.Value(schemas.BifrostContextKeyStreamEstimatedPromptTokens); got != nil {
		t.Fatalf("image stream should not receive token estimate, got %v", got)
	}
}

func TestEstimateStreamPromptTokensCountsToolAndReasoningReplay(t *testing.T) {
	model := "gpt-5.6-luna"
	role := schemas.ResponsesInputMessageRoleUser
	estimate := func(req *schemas.BifrostRequest) int {
		ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
		EstimateStreamPromptTokens(ctx, req)
		got, _ := ctx.Value(schemas.BifrostContextKeyStreamEstimatedPromptTokens).(int)
		return got
	}
	build := func(extra ...schemas.ResponsesMessage) *schemas.BifrostRequest {
		input := []schemas.ResponsesMessage{{
			Role:    &role,
			Content: &schemas.ResponsesMessageContent{ContentStr: ptr("hello")},
		}}
		input = append(input, extra...)
		return &schemas.BifrostRequest{
			RequestType: schemas.ResponsesStreamRequest,
			ResponsesRequest: &schemas.BifrostResponsesRequest{
				Model: model,
				Input: input,
			},
		}
	}

	outputText := strings.Repeat("tokenized tool output ", 200)
	call := schemas.ResponsesMessage{
		Type: ptr(schemas.ResponsesMessageTypeFunctionCall),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			Name:      ptr("shell"),
			Arguments: ptr(`{"command":"ls -la","workdir":"/tmp"}`),
		},
	}
	output := schemas.ResponsesMessage{
		Type: ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			Output: &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: &outputText},
		},
	}
	reasoning := schemas.ResponsesMessage{
		Type: ptr(schemas.ResponsesMessageTypeReasoning),
		ResponsesReasoning: &schemas.ResponsesReasoning{
			Summary: []schemas.ResponsesReasoningSummary{{
				Type: schemas.ResponsesReasoningContentBlockTypeSummaryText,
				Text: "thinking hard about the tool result",
			}},
			EncryptedContent: ptr("gAAAAAB" + strings.Repeat("qiUZ0vypxGjo86Zi_", 300)),
		},
	}

	baseline := estimate(build())
	withReplay := estimate(build(call, output, reasoning))

	if withReplay <= baseline {
		t.Fatalf("replay items not counted: baseline=%d withReplay=%d", baseline, withReplay)
	}
	// 工具输出是纯文本，回放的估算增量至少要覆盖它单独估算量的八成
	minDiff := EstimateTokensByModel(model, outputText) * 4 / 5
	if diff := withReplay - baseline; diff < minDiff {
		t.Fatalf("replay contribution too small: diff=%d want>=%d (baseline=%d withReplay=%d)", diff, minDiff, baseline, withReplay)
	}
}

func ptr[T any](v T) *T { return &v }

func TestEstimateStreamPromptTokensDoesNotCountImagePayloadAsText(t *testing.T) {
	image := strings.Repeat("A", 400_000)
	imageType := schemas.ResponsesInputMessageContentBlockTypeImage
	role := schemas.ResponsesInputMessageRoleUser
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	request := &schemas.BifrostRequest{
		RequestType: schemas.ResponsesStreamRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{
			Model: "gpt-5.6-luna",
			Input: []schemas.ResponsesMessage{{
				Role: &role,
				Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{{
					Type:                                   imageType,
					ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{ImageURL: &image},
				}}},
			}},
		},
	}

	EstimateStreamPromptTokens(ctx, request)

	got, _ := ctx.Value(schemas.BifrostContextKeyStreamEstimatedPromptTokens).(int)
	if got >= 10_000 {
		t.Fatalf("image payload was counted as text: estimated %d tokens", got)
	}
	details, _ := ctx.Value(schemas.BifrostContextKeyStreamEstimatedPromptDetails).(*schemas.ChatPromptTokensDetails)
	if details == nil || details.ImageTokens == 0 {
		t.Fatalf("expected image token details, got %+v", details)
	}
}
