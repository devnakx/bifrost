package utils

import (
	"math"
	"strings"
	"unicode"
)

type estimatedTokenProvider int

const (
	estimatedOpenAI estimatedTokenProvider = iota
	estimatedGemini
	estimatedClaude
)

type estimatedTokenMultipliers struct {
	word       float64
	number     float64
	cjk        float64
	symbol     float64
	mathSymbol float64
	urlDelim   float64
	atSign     float64
	emoji      float64
	newline    float64
	space      float64
}

var estimatedTokenMultipliersByProvider = map[estimatedTokenProvider]estimatedTokenMultipliers{
	estimatedGemini: {
		word: 1.15, number: 2.8, cjk: 0.68, symbol: 0.38, mathSymbol: 1.05,
		urlDelim: 1.2, atSign: 2.5, emoji: 1.08, newline: 1.15, space: 0.2,
	},
	estimatedClaude: {
		word: 1.13, number: 1.63, cjk: 1.21, symbol: 0.4, mathSymbol: 4.52,
		urlDelim: 1.26, atSign: 2.82, emoji: 2.6, newline: 0.89, space: 0.39,
	},
	estimatedOpenAI: {
		word: 1.02, number: 1.55, cjk: 0.85, symbol: 0.4, mathSymbol: 2.68,
		urlDelim: 1.0, atSign: 2.0, emoji: 2.12, newline: 0.5, space: 0.42,
	},
}

const (
	wordTypeNone = iota
	wordTypeLatin
	wordTypeNumber
)

// streamTokenEstimator 跨流式 chunk 保存单词边界和累计权重，避免把完整输出文本放进上下文
type streamTokenEstimator struct {
	multipliers estimatedTokenMultipliers
	wordType    int
	count       float64
}

func newStreamTokenEstimator(model string) *streamTokenEstimator {
	return &streamTokenEstimator{
		multipliers: multipliersForModel(model),
	}
}

func (e *streamTokenEstimator) Add(text string) {
	if e == nil || text == "" {
		return
	}
	e.count, e.wordType = addEstimatedTokenText(e.multipliers, e.count, e.wordType, text)
}

func (e *streamTokenEstimator) Tokens() int {
	if e == nil {
		return 0
	}
	return int(math.Ceil(e.count))
}

// EstimateTokensByModel 移植 new-api 的本地供应商加权估算规则
func EstimateTokensByModel(model, text string) int {
	if text == "" {
		return 0
	}

	m := multipliersForModel(model)
	count, _ := addEstimatedTokenText(m, 0, wordTypeNone, text)
	return int(math.Ceil(count))
}

func multipliersForModel(model string) estimatedTokenMultipliers {
	provider := estimatedOpenAI
	model = strings.ToLower(model)
	if strings.Contains(model, "gemini") {
		provider = estimatedGemini
	} else if strings.Contains(model, "claude") {
		provider = estimatedClaude
	}
	return estimatedTokenMultipliersByProvider[provider]
}

func addEstimatedTokenText(m estimatedTokenMultipliers, count float64, wordType int, text string) (float64, int) {
	for _, r := range text {
		if unicode.IsSpace(r) {
			wordType = wordTypeNone
			if r == '\n' || r == '\t' {
				count += m.newline
			} else {
				count += m.space
			}
			continue
		}

		if isEstimatedCJK(r) {
			wordType = wordTypeNone
			count += m.cjk
			continue
		}
		if isEstimatedEmoji(r) {
			wordType = wordTypeNone
			count += m.emoji
			continue
		}
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			newWordType := wordTypeLatin
			if unicode.IsNumber(r) {
				newWordType = wordTypeNumber
			}
			if wordType == wordTypeNone || wordType != newWordType {
				if newWordType == wordTypeNumber {
					count += m.number
				} else {
					count += m.word
				}
				wordType = newWordType
			}
			continue
		}

		wordType = wordTypeNone
		switch {
		case isEstimatedMathSymbol(r):
			count += m.mathSymbol
		case r == '@':
			count += m.atSign
		case isEstimatedURLDelimiter(r):
			count += m.urlDelim
		default:
			count += m.symbol
		}
	}
	return count, wordType
}

func isEstimatedCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		(r >= 0x3040 && r <= 0x30FF) ||
		(r >= 0xAC00 && r <= 0xD7A3)
}

func isEstimatedEmoji(r rune) bool {
	return (r >= 0x1F300 && r <= 0x1F9FF) ||
		(r >= 0x2600 && r <= 0x26FF) ||
		(r >= 0x2700 && r <= 0x27BF) ||
		(r >= 0x1FA00 && r <= 0x1FAFF)
}

func isEstimatedMathSymbol(r rune) bool {
	if strings.ContainsRune("∑∫∂√∞≤≥≠≈±×÷∈∉∋∌⊂⊃⊆⊇∪∩∧∨¬∀∃∄∅∆∇∝∟∠∡∢°′″‴⁺⁻⁼⁽⁾ⁿ₀₁₂₃₄₅₆₇₈₉₊₋₌₍₎²³¹⁴⁵⁶⁷⁸⁹⁰", r) {
		return true
	}
	return (r >= 0x2200 && r <= 0x22FF) ||
		(r >= 0x2A00 && r <= 0x2AFF) ||
		(r >= 0x1D400 && r <= 0x1D7FF)
}

func isEstimatedURLDelimiter(r rune) bool {
	return strings.ContainsRune("/:?&=;#%", r)
}
