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
