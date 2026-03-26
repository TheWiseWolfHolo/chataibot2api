package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type chatCompletionRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
	Size     string        `json:"size"`
	Image    string        `json:"image"`
	Images   []string      `json:"images"`
}

type chatMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type chatMessagePart struct {
	Type     string       `json:"type"`
	Text     string       `json:"text"`
	ImageURL chatImageURL `json:"image_url"`
}

type chatImageURL struct {
	URL string `json:"url"`
}

func isSupportedImageModel(model string) bool {
	_, ok := modelRouter[strings.TrimSpace(model)]
	return ok
}

func (a *App) HandleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req chatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "Request body must be valid JSON", "invalid_request_error")
		return
	}

	model := strings.TrimSpace(req.Model)
	if !isSupportedImageModel(model) {
		writeOpenAIError(w, http.StatusBadRequest, "text chat is not supported by this service", "unsupported_text_chat")
		return
	}

	prompt, sources, err := extractPromptAndImageSources(req)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}

	normalizedSources := make([]string, 0, len(sources))
	for _, source := range sources {
		normalized, normErr := normalizeImageSource(source)
		if normErr != nil {
			writeOpenAIError(w, http.StatusBadRequest, normErr.Error(), "invalid_request_error")
			return
		}
		normalizedSources = append(normalizedSources, normalized)
	}

	imageReq := OpenAIImageReq{
		Prompt: prompt,
		Model:  model,
		Size:   req.Size,
	}
	if len(normalizedSources) == 1 {
		imageReq.Image = normalizedSources[0]
	}
	if len(normalizedSources) > 1 {
		imageReq.Images = normalizedSources
	}

	resp, genErr := a.Generate(imageReq)
	if genErr != nil {
		writeOpenAIError(w, statusCodeForError(genErr), genErr.Error(), "generation_error")
		return
	}

	markdown := buildMarkdownImageContent(resp.Data)
	if req.Stream {
		writeChatCompletionStream(w, req.Model, markdown, resp.Created)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id":      fmt.Sprintf("chatcmpl-%d", resp.Created),
		"object":  "chat.completion",
		"created": resp.Created,
		"model":   req.Model,
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": markdown,
				},
				"finish_reason": "stop",
			},
		},
	})
}

func extractPromptAndImageSources(req chatCompletionRequest) (string, []string, error) {
	sources := make([]string, 0, len(req.Images)+1)
	appendUniqueString(&sources, req.Image)
	for _, image := range req.Images {
		appendUniqueString(&sources, image)
	}

	prompt := ""
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role != "user" {
			continue
		}
		text, extracted := extractPromptAndImagesFromMessage(req.Messages[i].Content)
		if prompt == "" {
			prompt = text
		}
		for _, source := range extracted {
			appendUniqueString(&sources, source)
		}
		break
	}

	if strings.TrimSpace(prompt) == "" {
		return "", nil, fmt.Errorf("image-compatible chat request requires a user prompt")
	}

	return prompt, sources, nil
}

func extractPromptAndImagesFromMessage(raw json.RawMessage) (string, []string) {
	if len(raw) == 0 {
		return "", nil
	}

	var textContent string
	if err := json.Unmarshal(raw, &textContent); err == nil {
		return strings.TrimSpace(textContent), nil
	}

	var parts []chatMessagePart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", nil
	}

	texts := make([]string, 0, len(parts))
	sources := make([]string, 0, len(parts))
	for _, part := range parts {
		switch strings.ToLower(strings.TrimSpace(part.Type)) {
		case "text":
			if trimmed := strings.TrimSpace(part.Text); trimmed != "" {
				texts = append(texts, trimmed)
			}
		case "image_url":
			appendUniqueString(&sources, part.ImageURL.URL)
		}
	}

	return strings.Join(texts, "\n"), sources
}

func normalizeImageSource(source string) (string, error) {
	trimmed := strings.TrimSpace(source)
	if trimmed == "" {
		return "", fmt.Errorf("image source cannot be empty")
	}
	if strings.HasPrefix(trimmed, "data:") {
		return trimmed, nil
	}
	if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
		resp, err := http.Get(trimmed)
		if err != nil {
			return "", fmt.Errorf("failed to fetch image URL: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("failed to fetch image URL: status %d", resp.StatusCode)
		}

		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", fmt.Errorf("failed to read image URL: %w", err)
		}
		contentType := resp.Header.Get("Content-Type")
		if strings.TrimSpace(contentType) == "" {
			contentType = http.DetectContentType(data)
		}

		return fmt.Sprintf("data:%s;base64,%s", contentType, base64.StdEncoding.EncodeToString(data)), nil
	}

	return trimmed, nil
}

func buildMarkdownImageContent(data []ImageData) string {
	lines := make([]string, 0, len(data))
	for _, item := range data {
		if strings.TrimSpace(item.URL) == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("![](%s)", item.URL))
	}
	return strings.Join(lines, "\n")
}

func writeChatCompletionStream(w http.ResponseWriter, model string, content string, created int64) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	chunks := []map[string]any{
		{
			"id":      fmt.Sprintf("chatcmpl-%d", created),
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model,
			"choices": []map[string]any{
				{
					"index": 0,
					"delta": map[string]any{
						"role":    "assistant",
						"content": content,
					},
					"finish_reason": nil,
				},
			},
		},
		{
			"id":      fmt.Sprintf("chatcmpl-%d", created),
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model,
			"choices": []map[string]any{
				{
					"index":         0,
					"delta":         map[string]any{},
					"finish_reason": "stop",
				},
			},
		},
	}

	for _, chunk := range chunks {
		payload, _ := json.Marshal(chunk)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
}

func appendUniqueString(target *[]string, value string) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return
	}
	for _, existing := range *target {
		if existing == trimmed {
			return
		}
	}
	*target = append(*target, trimmed)
}
