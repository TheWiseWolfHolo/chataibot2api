package main

import "strings"

var publicToRawModelID = map[string]string{
	"seedream-4.0":                   "BYTEDANCE-seedream-4",
	"seedream-5.0-lite":              "BYTEDANCE-seedream-5-lite",
	"flux-1.1-pro":                   "FLUX-pro",
	"flux-schnell":                   "FLUX-schnell",
	"flux-ultra":                     "FLUX-ultra",
	"gemini-2.5-flash-image":         "GOOGLE-nano-banana",
	"gemini-3.1-flash-image-preview": "GOOGLE-nano-banana-2",
	"gpt-image-1":                    "GPT_IMAGE",
	"gpt-image-1.5":                  "GPT_IMAGE_1_5",
	"grok-4.1-fast":                  "grok",
	"grok-imagine-1.0":               "GROK",
	"ideogram-3":                     "IDEOGRAM",
	"ideogram-3-turbo":               "IDEOGRAM_TURBO",
	"qwen-image(lora)":               "QWEN-lora",
	"claude-haiku-3":                 "claude-3-haiku",
	"claude-sonnet-3":                "claude-3-sonnet",
	"claude-haiku-4.5":               "claude-4.5-haiku",
	"claude-sonnet-4.6":              "claude-4.6-sonnet",
	"deepseek-r1":                    "deepseek",
	"deepseek-v3.2":                  "deepseek-v3.2",
	"gemini-2.0-flash-search":        "gemini-2-flash-search",
	"gemini-3-flash-preview":         "gemini-3-flash",
	"gemini-3-flash-preview-search":  "gemini-3-flash-search",
	"gemini-2.0-flash":               "gemini-flash",
	"gemini-2.5-pro":                 "gemini-pro",
	"gpt-4.1":                        "gpt-4.1",
	"gpt-4.1-nano":                   "gpt-4.1-nano",
	"gpt-4o-search-preview":          "gpt-4o-search-preview",
	"gpt-5.1":                        "gpt-5.1",
	"gpt-5.2":                        "gpt-5.2",
	"gpt-5.4":                        "gpt-5.4",
	"gpt-5.4-mini":                   "gpt-5.4-mini",
	"gpt-5.4-nano":                   "gpt-5.4-nano",
	"o4-mini":                        "o4-mini",
	"perplexity":                     "perplexity",
	"qwen-3-235b-a22b-thinking-2507": "qwen3-thinking-2507",
	"qwen-3.5-397b-a17b":             "qwen3.5",
}

var rawToPublicModelID = func() map[string]string {
	out := make(map[string]string, len(publicToRawModelID))
	for publicID, rawID := range publicToRawModelID {
		if _, exists := out[rawID]; !exists {
			out[rawID] = publicID
		}
	}
	return out
}()

func resolveRawModelID(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	if rawID, ok := publicToRawModelID[model]; ok {
		return rawID
	}
	return model
}

func publicModelID(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	if _, ok := publicToRawModelID[model]; ok {
		return model
	}
	if publicID, ok := rawToPublicModelID[model]; ok {
		return publicID
	}
	return model
}
