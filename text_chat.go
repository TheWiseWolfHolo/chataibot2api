package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const syntheticStreamChunkGap = 24 * time.Millisecond

func (a *App) handleTextChatCompletions(w http.ResponseWriter, req chatCompletionRequest) {
	publicID := publicModelID(req.Model)
	if req.Stream {
		writer := &openAITextStreamWriter{
			w:       w,
			model:   publicID,
			created: a.now().Unix(),
		}
		if shouldEmitImmediateRolePrelude(req.Model) {
			if err := writer.WriteRole(); err != nil {
				writeOpenAIError(w, statusCodeForError(err), err.Error(), errorTypeForError(err, "generation_error"))
				return
			}
		}
		resp, err := a.StreamTextChat(req, func(event TextStreamEvent) error {
			if strings.EqualFold(strings.TrimSpace(event.Type), "botType") {
				if strings.TrimSpace(event.ChatModel) != "" {
					writer.model = publicModelID(event.ChatModel)
				}
				return writer.WriteRole()
			}
			if strings.EqualFold(strings.TrimSpace(event.Type), "reasoningContent") {
				return writer.WriteReasoningDelta(event.ReasoningContent)
			}
			if strings.EqualFold(strings.TrimSpace(event.Type), "chunk") {
				return writer.WriteDelta(event.Delta)
			}
			return nil
		})
		if err != nil {
			if !writer.wroteContent && (isTextTimeoutError(err) || isRetryableTextTransportError(err) || errorTypeForError(err, "") == "upstream_timeout") {
				if !writer.started {
					w.Header().Set("X-Holo-Text-Stream-Mode", "synthetic-fallback")
				}
				resp, fallbackErr := a.CompleteTextChat(req)
				if fallbackErr != nil {
					if !writer.started {
						writeOpenAIError(w, statusCodeForError(fallbackErr), fallbackErr.Error(), errorTypeForError(fallbackErr, "generation_error"))
					}
					return
				}
				if strings.TrimSpace(resp.ChatModel) != "" {
					writer.model = publicModelID(resp.ChatModel)
				}
				if strings.TrimSpace(resp.ReasoningContent) != "" {
					if err := writer.WriteReasoningDelta(resp.ReasoningContent); err != nil {
						if !writer.started {
							writeOpenAIError(w, statusCodeForError(err), err.Error(), errorTypeForError(err, "generation_error"))
						}
						return
					}
				}
				if err := writer.WriteDelta(resp.Content); err != nil {
					if !writer.started {
						writeOpenAIError(w, statusCodeForError(err), err.Error(), errorTypeForError(err, "generation_error"))
					}
					return
				}
				if err := writer.Finish(); err != nil && !writer.started {
					writeOpenAIError(w, statusCodeForError(err), err.Error(), errorTypeForError(err, "generation_error"))
				}
				return
			}
			if writer.started {
				_ = writer.Finish()
				return
			}
			if !writer.started {
				writeOpenAIError(w, statusCodeForError(err), err.Error(), errorTypeForError(err, "generation_error"))
			}
			return
		}
		if !writer.wroteContent && strings.TrimSpace(resp.Content) != "" {
			if err := writer.WriteDelta(resp.Content); err != nil {
				if !writer.started {
					writeOpenAIError(w, statusCodeForError(err), err.Error(), errorTypeForError(err, "generation_error"))
				}
				return
			}
		}
		if err := writer.Finish(); err != nil && !writer.started {
			writeOpenAIError(w, statusCodeForError(err), err.Error(), errorTypeForError(err, "generation_error"))
		}
		return
	}

	resp, err := a.CompleteTextChat(req)
	if err != nil {
		writeOpenAIError(w, statusCodeForError(err), err.Error(), errorTypeForError(err, "generation_error"))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id":      fmt.Sprintf("chatcmpl-%d", a.now().Unix()),
		"object":  "chat.completion",
		"created": a.now().Unix(),
		"model":   publicID,
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": resp.Content,
				},
				"finish_reason": "stop",
			},
		},
	})
}

func buildTextUpstreamRequest(req chatCompletionRequest) (UpstreamTextMessageRequest, string, int, error) {
	model := strings.TrimSpace(req.Model)
	modelCfg, exists := lookupTextModel(model)
	if !exists || modelCfg.Hidden {
		return UpstreamTextMessageRequest{}, "", 0, fmt.Errorf("Unsupported model: %s", model)
	}

	if strings.TrimSpace(req.Image) != "" || len(req.Images) > 0 {
		return UpstreamTextMessageRequest{}, "", 0, fmt.Errorf("text chat image inputs are not supported by this service")
	}

	prompt, title, err := flattenTextMessages(req.Messages)
	if err != nil {
		return UpstreamTextMessageRequest{}, "", 0, err
	}

	return UpstreamTextMessageRequest{
		Text:                   prompt,
		Model:                  model,
		WithPotentialQuestions: false,
	}, title, modelCfg.Cost, nil
}

func flattenTextMessages(messages []chatMessage) (string, string, error) {
	parts := make([]string, 0, len(messages))
	lastUserText := ""

	for _, message := range messages {
		text, hasImage, err := extractTextOnlyFromMessage(message.Content)
		if err != nil {
			return "", "", err
		}
		if hasImage {
			return "", "", fmt.Errorf("text chat image inputs are not supported by this service")
		}
		if strings.TrimSpace(text) == "" {
			continue
		}

		role := strings.ToUpper(strings.TrimSpace(message.Role))
		if role == "" {
			role = "USER"
		}
		parts = append(parts, fmt.Sprintf("%s:\n%s", role, text))
		if strings.EqualFold(strings.TrimSpace(message.Role), "user") {
			lastUserText = text
		}
	}

	if len(parts) == 0 {
		return "", "", fmt.Errorf("text chat requires at least one text message")
	}

	titleSource := lastUserText
	if strings.TrimSpace(titleSource) == "" {
		titleSource = parts[len(parts)-1]
	}
	return strings.Join(parts, "\n\n"), deriveChatTitle(titleSource), nil
}

func extractTextOnlyFromMessage(raw json.RawMessage) (string, bool, error) {
	if len(raw) == 0 {
		return "", false, nil
	}

	var textContent string
	if err := json.Unmarshal(raw, &textContent); err == nil {
		return strings.TrimSpace(textContent), false, nil
	}

	var parts []chatMessagePart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", false, fmt.Errorf("unsupported chat message content format")
	}

	texts := make([]string, 0, len(parts))
	hasImage := false
	for _, part := range parts {
		switch strings.ToLower(strings.TrimSpace(part.Type)) {
		case "text":
			if trimmed := strings.TrimSpace(part.Text); trimmed != "" {
				texts = append(texts, trimmed)
			}
		case "image_url":
			hasImage = true
		}
	}

	return strings.Join(texts, "\n"), hasImage, nil
}

func deriveChatTitle(input string) string {
	normalized := strings.Join(strings.Fields(strings.ReplaceAll(input, "\n", " ")), " ")
	if normalized == "" {
		return "New chat"
	}
	runes := []rune(normalized)
	if len(runes) > 80 {
		return string(runes[:80])
	}
	return normalized
}

func shouldEmitImmediateRolePrelude(model string) bool {
	model = strings.TrimSpace(resolveRawModelID(model))
	if cfg, ok := lookupTextModel(model); ok && cfg.Internet {
		return true
	}
	switch model {
	case "gpt-5.1", "gpt-5.2", "gpt-5.4",
		"gpt-5.1-high", "gpt-5.2-high", "gpt-5.4-high", "gpt-5.4-pro",
		"o3", "o3-pro", "o4-mini-deep-research",
		"qwen3-thinking-2507", "qwen3-max",
		"gemini-pro", "gemini-3-pro", "gemini-3.1-pro",
		"claude-3-opus", "claude-4.5-opus", "claude-4.6-opus",
		"claude-3-sonnet-high", "claude-4.6-sonnet-high":
		return true
	default:
		return false
	}
}

type openAITextStreamWriter struct {
	w            http.ResponseWriter
	model        string
	created      int64
	started      bool
	wroteRole    bool
	wroteContent bool
}

func (s *openAITextStreamWriter) start() {
	if s.started {
		return
	}
	s.w.Header().Set("Content-Type", "text/event-stream")
	s.w.Header().Set("Cache-Control", "no-cache")
	s.w.Header().Set("Connection", "keep-alive")
	s.w.WriteHeader(http.StatusOK)
	s.started = true
	s.flush()
}

func (s *openAITextStreamWriter) WriteDelta(content string) error {
	if content == "" {
		return nil
	}
	s.start()
	if !s.wroteRole {
		if err := s.WriteRole(); err != nil {
			return err
		}
	}
	s.wroteContent = true

	pieces := splitTextStreamDelta(content)
	for index, piece := range pieces {
		if piece == "" {
			continue
		}
		payload, err := json.Marshal(map[string]any{
			"id":      fmt.Sprintf("chatcmpl-%d", s.created),
			"object":  "chat.completion.chunk",
			"created": s.created,
			"model":   s.model,
			"choices": []map[string]any{
				{
					"index": 0,
					"delta": map[string]any{
						"content": piece,
					},
					"finish_reason": nil,
				},
			},
		})
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(s.w, "data: %s\n\n", payload); err != nil {
			return err
		}
		s.flush()
		if len(pieces) > 1 && index < len(pieces)-1 {
			time.Sleep(syntheticStreamChunkGap)
		}
	}
	return nil
}

func (s *openAITextStreamWriter) WriteReasoningDelta(content string) error {
	if content == "" {
		return nil
	}
	s.start()
	if !s.wroteRole {
		if err := s.WriteRole(); err != nil {
			return err
		}
	}

	pieces := splitTextStreamDelta(content)
	for index, piece := range pieces {
		if piece == "" {
			continue
		}
		payload, err := json.Marshal(map[string]any{
			"id":      fmt.Sprintf("chatcmpl-%d", s.created),
			"object":  "chat.completion.chunk",
			"created": s.created,
			"model":   s.model,
			"choices": []map[string]any{
				{
					"index": 0,
					"delta": map[string]any{
						"reasoning_content": piece,
					},
					"finish_reason": nil,
				},
			},
		})
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(s.w, "data: %s\n\n", payload); err != nil {
			return err
		}
		s.flush()
		if len(pieces) > 1 && index < len(pieces)-1 {
			time.Sleep(syntheticStreamChunkGap)
		}
	}
	return nil
}

func (s *openAITextStreamWriter) WriteRole() error {
	if s.wroteRole {
		return nil
	}
	s.start()

	payload, err := json.Marshal(map[string]any{
		"id":      fmt.Sprintf("chatcmpl-%d", s.created),
		"object":  "chat.completion.chunk",
		"created": s.created,
		"model":   s.model,
		"choices": []map[string]any{
			{
				"index": 0,
				"delta": map[string]any{
					"role": "assistant",
				},
				"finish_reason": nil,
			},
		},
	})
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", payload); err != nil {
		return err
	}
	s.wroteRole = true
	s.flush()
	return nil
}

func (s *openAITextStreamWriter) Finish() error {
	s.start()

	payload, err := json.Marshal(map[string]any{
		"id":      fmt.Sprintf("chatcmpl-%d", s.created),
		"object":  "chat.completion.chunk",
		"created": s.created,
		"model":   s.model,
		"choices": []map[string]any{
			{
				"index":         0,
				"delta":         map[string]any{},
				"finish_reason": "stop",
			},
		},
	})
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", payload); err != nil {
		return err
	}
	if _, err := fmt.Fprint(s.w, "data: [DONE]\n\n"); err != nil {
		return err
	}
	s.flush()
	return nil
}

func (s *openAITextStreamWriter) flush() {
	flusher, ok := s.w.(http.Flusher)
	if ok {
		flusher.Flush()
	}
}

func splitTextStreamDelta(content string) []string {
	if content == "" {
		return nil
	}

	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	if strings.Contains(normalized, "\n") {
		lines := strings.SplitAfter(normalized, "\n")
		chunks := make([]string, 0, len(lines))
		for _, line := range lines {
			if line == "" {
				continue
			}
			chunks = append(chunks, splitLongStreamSegment(line)...)
		}
		return chunks
	}

	return splitLongStreamSegment(normalized)
}

func splitLongStreamSegment(segment string) []string {
	if segment == "" {
		return nil
	}
	if utf8.RuneCountInString(segment) <= 48 {
		return []string{segment}
	}

	runes := []rune(segment)
	const (
		targetChunkSize = 36
		maxChunkSize    = 48
	)

	chunks := make([]string, 0, (len(runes)/targetChunkSize)+1)
	for start := 0; start < len(runes); {
		end := start + targetChunkSize
		if end >= len(runes) {
			chunks = append(chunks, string(runes[start:]))
			break
		}

		cut := end
		limit := minInt(len(runes), start+maxChunkSize)
		for i := limit - 1; i > start+12; i-- {
			if isStreamSplitBoundary(runes[i]) {
				cut = i + 1
				break
			}
		}
		if cut <= start {
			cut = limit
		}
		chunks = append(chunks, string(runes[start:cut]))
		start = cut
	}

	return chunks
}

func isStreamSplitBoundary(r rune) bool {
	switch r {
	case ' ', '\t', ',', '，', '.', '。', '!', '！', '?', '？', ';', '；', ':', '：', ')', '）', ']', '】', '}', '》':
		return true
	default:
		return false
	}
}
