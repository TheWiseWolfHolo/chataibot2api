package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

func (a *App) handleTextChatCompletions(w http.ResponseWriter, req chatCompletionRequest) {
	if req.Stream {
		writer := &openAITextStreamWriter{
			w:       w,
			model:   req.Model,
			created: a.now().Unix(),
		}
		resp, err := a.StreamTextChat(req, func(event TextStreamEvent) error {
			if strings.EqualFold(strings.TrimSpace(event.Type), "chunk") {
				return writer.WriteDelta(event.Delta)
			}
			return nil
		})
		if err != nil {
			if !writer.started {
				writeOpenAIError(w, statusCodeForError(err), err.Error(), "generation_error")
			}
			return
		}
		if !writer.wroteContent && strings.TrimSpace(resp.Content) != "" {
			if err := writer.WriteDelta(resp.Content); err != nil {
				if !writer.started {
					writeOpenAIError(w, statusCodeForError(err), err.Error(), "generation_error")
				}
				return
			}
		}
		if err := writer.Finish(); err != nil && !writer.started {
			writeOpenAIError(w, statusCodeForError(err), err.Error(), "generation_error")
		}
		return
	}

	resp, err := a.CompleteTextChat(req)
	if err != nil {
		writeOpenAIError(w, statusCodeForError(err), err.Error(), "generation_error")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id":      fmt.Sprintf("chatcmpl-%d", a.now().Unix()),
		"object":  "chat.completion",
		"created": a.now().Unix(),
		"model":   req.Model,
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
	modelCfg, exists := textModelRouter[model]
	if !exists {
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

type openAITextStreamWriter struct {
	w            http.ResponseWriter
	model        string
	created      int64
	started      bool
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
	s.wroteContent = true

	payload, err := json.Marshal(map[string]any{
		"id":      fmt.Sprintf("chatcmpl-%d", s.created),
		"object":  "chat.completion.chunk",
		"created": s.created,
		"model":   s.model,
		"choices": []map[string]any{
			{
				"index": 0,
				"delta": map[string]any{
					"content": content,
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
