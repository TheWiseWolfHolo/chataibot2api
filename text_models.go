package main

import "strings"

type TextModelConfig struct {
	Cost     int
	Internet bool
	Hidden   bool
}

var textModelRouter = map[string]TextModelConfig{
	"claude-3-haiku":         {Cost: 1},
	"gemini-flash":           {Cost: 1},
	"gpt-4.1-nano":           {Cost: 1},
	"o4-mini":                {Cost: 2},
	"gpt-4.1":                {Cost: 3},
	"gpt-5.1":                {Cost: 3},
	"gpt-5.2":                {Cost: 4},
	"gemini-3-pro":           {Cost: 15, Hidden: true},
	"gemini-3.1-pro":         {Cost: 15, Hidden: true},
	"gemini-3-flash":         {Cost: 4},
	"gemini-pro":             {Cost: 10},
	"claude-3-opus":          {Cost: 20, Hidden: true},
	"o3-pro":                 {Cost: 25, Hidden: true},
	"deepseek":               {Cost: 1},
	"deepseek-v3.2":          {Cost: 2},
	"claude-3-sonnet":        {Cost: 4},
	"claude-4.6-sonnet":      {Cost: 4},
	"claude-4.5-opus":        {Cost: 7, Hidden: true},
	"claude-4.6-opus":        {Cost: 7, Hidden: true},
	"claude-4.5-haiku":       {Cost: 3},
	"gpt-5.1-high":           {Cost: 15, Hidden: true},
	"gpt-5.2-high":           {Cost: 22, Hidden: true},
	"claude-4.6-sonnet-high": {Cost: 10, Hidden: true},
	"claude-3-sonnet-high":   {Cost: 10, Hidden: true},
	"grok":                   {Cost: 1},
	"o3":                     {Cost: 3, Hidden: true},
	"qwen3-thinking-2507":    {Cost: 2},
	"qwen3-max":              {Cost: 10, Hidden: true},
	"qwen3.5":                {Cost: 4},
	"qwen3.5-plus":           {Cost: 7, Hidden: true},
	"gpt-5.4-nano":           {Cost: 1},
	"gpt-5.4-mini":           {Cost: 3},
	"gpt-5.4":                {Cost: 6},
	"gpt-5.4-high":           {Cost: 30, Hidden: true},
	"gpt-5.4-pro":            {Cost: 70, Hidden: true},
	"o4-mini-deep-research":  {Cost: 50, Internet: true, Hidden: true},
	"gpt-4o-search-preview":  {Cost: 15, Internet: true},
	"perplexity-pro":         {Cost: 20, Internet: true, Hidden: true},
	"perplexity":             {Cost: 2, Internet: true},
	"gemini-2-flash-search":  {Cost: 2, Internet: true},
	"gemini-3-flash-search":  {Cost: 6, Internet: true},
}

func lookupTextModel(model string) (TextModelConfig, bool) {
	cfg, ok := textModelRouter[strings.TrimSpace(model)]
	return cfg, ok
}

func isSupportedTextModel(model string) bool {
	cfg, ok := lookupTextModel(model)
	return ok && !cfg.Hidden
}
