package utils

import (
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
		{name: "openai word", model: "gpt-4o", text: "hello", want: 2},
		{name: "claude cjk", model: "claude-3-7-sonnet", text: "你好", want: 3},
		{name: "gemini emoji", model: "gemini-2.5-pro", text: "😀", want: 2},
		{name: "openai number", model: "gpt-4o", text: "123", want: 2},
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
	whole := EstimateTokensByModel("gpt-4o", "hello world")
	estimator := newStreamTokenEstimator("gpt-4o")
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
