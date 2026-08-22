package utils

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// streamPromptEstimate 保存取消计费所需的输入估算和媒体明细
type streamPromptEstimate struct {
	promptTokens int
	textTokens   int
	imageTokens  int
	audioTokens  int
}

// estimateStreamPromptUsage 按 new-api 的方式拆分文本、工具和媒体，避免把完整 JSON 当作文本计数
func estimateStreamPromptUsage(request *schemas.BifrostRequest) *streamPromptEstimate {
	if request == nil {
		return nil
	}
	model := requestModel(request)
	var text strings.Builder
	imageTokens := 0
	audioCount := 0
	messageCount := 0
	nameCount := 0

	appendText := func(value string) {
		if value != "" {
			text.WriteString(value)
			text.WriteByte('\n')
		}
	}
	appendToolText := func(value string) {
		if value != "" {
			appendText(value)
		}
	}

	switch request.RequestType {
	case schemas.TextCompletionStreamRequest:
		if request.TextCompletionRequest != nil {
			if request.TextCompletionRequest.Input != nil {
				appendText(pointerString(request.TextCompletionRequest.Input.PromptStr))
				for _, prompt := range request.TextCompletionRequest.Input.PromptArray {
					appendText(prompt)
				}
			}
		}
	case schemas.ChatCompletionStreamRequest:
		if request.ChatRequest == nil {
			return nil
		}
		for _, message := range request.ChatRequest.Input {
			messageCount++
			if message.Name != nil {
				nameCount++
				appendText(*message.Name)
			}
			appendText(string(message.Role))
			if message.Content == nil {
				continue
			}
			if message.Content.ContentStr != nil {
				appendText(*message.Content.ContentStr)
			}
			for _, block := range message.Content.ContentBlocks {
				appendText(string(block.Type))
				appendText(pointerString(block.Text))
				appendText(pointerString(block.Refusal))
				if block.ImageURLStruct != nil {
					imageTokens += estimateImageTokens(model, block.ImageURLStruct.URL, pointerString(block.ImageURLStruct.Detail))
				}
				if block.InputAudio != nil {
					audioCount++
				}
				if block.File != nil && block.File.FileType != nil && strings.HasPrefix(*block.File.FileType, "image/") {
					imageTokens += estimateImageTokens(model, pointerString(block.File.FileData), "")
				}
			}
		}
		if request.ChatRequest.Params != nil {
			for _, tool := range request.ChatRequest.Params.Tools {
				appendToolText(string(tool.Type))
				if tool.Function != nil {
					appendToolText(tool.Function.Name)
					appendToolText(pointerString(tool.Function.Description))
					if tool.Function.Parameters != nil {
						if data, err := schemas.MarshalSorted(tool.Function.Parameters); err == nil {
							appendToolText(string(data))
						}
					}
				}
			}
		}
	case schemas.ResponsesStreamRequest:
		if request.ResponsesRequest == nil {
			return nil
		}
		for _, message := range request.ResponsesRequest.Input {
			messageCount++
			appendText(pointerString(message.Role))
			if message.Content == nil {
				continue
			}
			if message.Content.ContentStr != nil {
				appendText(*message.Content.ContentStr)
			}
			for _, block := range message.Content.ContentBlocks {
				appendText(string(block.Type))
				appendText(pointerString(block.Text))
				if block.ResponsesOutputMessageContentText != nil {
					for _, annotation := range block.ResponsesOutputMessageContentText.Annotations {
						appendText(pointerString(annotation.Text))
					}
				}
				switch block.Type {
				case schemas.ResponsesInputMessageContentBlockTypeImage:
					if block.ResponsesInputMessageContentBlockImage != nil {
						imageTokens += estimateImageTokens(model, pointerString(block.ImageURL), pointerString(block.Detail))
					} else {
						imageTokens += fallbackImageTokens(model)
					}
				case schemas.ResponsesInputMessageContentBlockTypeAudio:
					audioCount++
				}
			}
		}
		if params := request.ResponsesRequest.Params; params != nil {
			appendText(pointerString(params.Instructions))
			appendText(pointerString(params.ToolChoice))
			for _, tool := range params.Tools {
				appendToolText(string(tool.Type))
				appendToolText(pointerString(tool.Name))
				appendToolText(pointerString(tool.Description))
				if tool.ResponsesToolFunction != nil && tool.ResponsesToolFunction.Parameters != nil {
					if data, err := schemas.MarshalSorted(tool.ResponsesToolFunction.Parameters); err == nil {
						appendToolText(string(data))
					}
				}
			}
		}
	default:
		return nil
	}

	textTokens := EstimateTokensByModel(model, text.String())
	// 与 new-api 在无法读取媒体元数据时的保守兜底一致，不使用 URL 或 Base64 长度
	audioTokens := audioCount * 256
	formatTokens := messageCount*3 + nameCount*3
	return &streamPromptEstimate{
		promptTokens: textTokens + imageTokens + audioTokens + formatTokens,
		textTokens:   textTokens + formatTokens,
		imageTokens:  imageTokens,
		audioTokens:  audioTokens,
	}
}

func pointerString[T any](value *T) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(*value)
}

func fallbackImageTokens(model string) int {
	lower := strings.ToLower(model)
	if strings.HasPrefix(lower, "gpt-5") && !strings.Contains(lower, "mini") && !strings.Contains(lower, "nano") {
		return 210
	}
	return 255
}

// estimateImageTokens mirrors new-api's OpenAI image fallback and tile rules
func estimateImageTokens(model, source, detail string) int {
	baseTokens, tileTokens, multiplier, patchBased := imageTokenRules(model)
	if detail == "low" {
		return baseTokens
	}
	width, height, ok := decodeImageDimensions(source)
	if !ok {
		return 3 * baseTokens
	}
	if patchBased {
		patches := int(math.Ceil(float64(width)/32) * math.Ceil(float64(height)/32))
		if patches > 1536 {
			patches = 1536
		}
		return int(math.Round(float64(patches) * multiplier))
	}
	maxSide := math.Max(float64(width), float64(height))
	fitScale := 1.0
	if maxSide > 2048 {
		fitScale = maxSide / 2048
	}
	fitW := float64(width) / fitScale
	fitH := float64(height) / fitScale
	shortScale := 768 / math.Min(fitW, fitH)
	finalW := fitW * shortScale
	finalH := fitH * shortScale
	tiles := math.Ceil(finalW/512) * math.Ceil(finalH/512)
	return int(math.Round(float64(baseTokens) + tiles*float64(tileTokens)))
}

func imageTokenRules(model string) (base, tile int, multiplier float64, patchBased bool) {
	lower := strings.ToLower(model)
	base, tile, multiplier = 85, 170, 1
	switch {
	case strings.Contains(lower, "gpt-4.1-mini"), strings.Contains(lower, "gpt-5-mini"):
		patchBased, multiplier = true, 1.62
	case strings.Contains(lower, "gpt-4.1-nano"), strings.Contains(lower, "gpt-5-nano"):
		patchBased, multiplier = true, 2.46
	case strings.HasPrefix(lower, "o4-mini"):
		patchBased, multiplier = true, 1.72
	case strings.HasPrefix(lower, "gpt-5") && !strings.Contains(lower, "mini") && !strings.Contains(lower, "nano"):
		base, tile = 70, 140
	case strings.HasPrefix(lower, "o1"), strings.HasPrefix(lower, "o3"):
		base, tile = 75, 150
	case strings.HasPrefix(lower, "gpt-4o-mini"):
		base, tile = 2833, 5667
	}
	return base, tile, multiplier, patchBased
}

func decodeImageDimensions(source string) (int, int, bool) {
	comma := strings.IndexByte(source, ',')
	if comma < 0 || !strings.HasPrefix(source[:comma], "data:") {
		return 0, 0, false
	}
	data, err := base64.StdEncoding.DecodeString(source[comma+1:])
	if err != nil {
		data, err = base64.RawStdEncoding.DecodeString(source[comma+1:])
	}
	if err != nil {
		return 0, 0, false
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return 0, 0, false
	}
	return config.Width, config.Height, true
}
