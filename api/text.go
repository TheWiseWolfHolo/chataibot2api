package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"chataibot2api/protocol"
)

const (
	chataibotAPIBaseURL       = "https://chataibot.pro/api"
	upstreamFromWeb           = 1
	textContextTimeout        = 5 * time.Second
	textRequestTimeout        = 60 * time.Second
	textThinkingTimeout       = 3 * time.Minute
	textStreamTimeout         = 90 * time.Second
	textStreamThinkingTimeout = 4 * time.Minute
	textJobPollInterval       = 2 * time.Second
	textJobPollAttempts       = 8
)

func (c *APIClient) CreateChatContext(model, title, jwtToken string) (int, error) {
	payload := map[string]any{
		"title":     title,
		"chatModel": model,
	}

	req, err := c.newUpstreamJSONRequest(http.MethodPost, chataibotAPIBaseURL+"/message/context", payload, jwtToken)
	if err != nil {
		return 0, err
	}

	fastClient := *c.httpClient
	fastClient.Timeout = textContextTimeout

	resp, err := fastClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return 0, parseUpstreamError(resp.StatusCode, body)
	}

	var parsed struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return 0, fmt.Errorf("failed to parse chat context response: %s", strings.TrimSpace(string(body)))
	}
	if parsed.ID == 0 {
		return 0, fmt.Errorf("upstream returned empty chat context id: %s", strings.TrimSpace(string(body)))
	}

	return parsed.ID, nil
}

func (c *APIClient) SendTextMessage(req protocol.TextMessageRequest, jwtToken string) (protocol.TextCompletionResult, error) {
	httpReq, err := c.newUpstreamJSONRequest(http.MethodPost, chataibotAPIBaseURL+"/message", buildTextMessagePayload(req), jwtToken)
	if err != nil {
		return protocol.TextCompletionResult{}, err
	}

	slowClient := *c.httpClient
	slowClient.Timeout = textRequestTimeoutForModel(req.Model)

	resp, err := slowClient.Do(httpReq)
	if err != nil {
		return protocol.TextCompletionResult{}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return protocol.TextCompletionResult{}, parseUpstreamError(resp.StatusCode, body)
	}

	result, jobID, err := parseDirectTextCompletion(body)
	if err != nil {
		return protocol.TextCompletionResult{}, err
	}
	if jobID != 0 {
		return c.pollTextJob(jobID, jwtToken)
	}

	return result, nil
}

func (c *APIClient) StreamTextMessage(ctx context.Context, req protocol.TextMessageRequest, jwtToken string, emit func(protocol.TextStreamEvent) error) (protocol.TextCompletionResult, error) {
	httpReq, err := c.newUpstreamJSONRequest(http.MethodPost, chataibotAPIBaseURL+"/message/streaming", buildTextMessagePayload(req), jwtToken)
	if err != nil {
		return protocol.TextCompletionResult{}, err
	}
	if ctx != nil {
		httpReq = httpReq.WithContext(ctx)
	}
	httpReq.Header.Set("Accept", "text/event-stream")

	slowClient := *c.httpClient
	slowClient.Timeout = textStreamTimeoutForModel(req.Model)

	resp, err := slowClient.Do(httpReq)
	if err != nil {
		return protocol.TextCompletionResult{}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return protocol.TextCompletionResult{}, parseUpstreamError(resp.StatusCode, body)
	}

	decoder := json.NewDecoder(resp.Body)
	var (
		result           protocol.TextCompletionResult
		builder          strings.Builder
		reasoningBuilder strings.Builder
	)

	for {
		var frame struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if err := decoder.Decode(&frame); err != nil {
			if err == io.EOF {
				break
			}
			return protocol.TextCompletionResult{}, fmt.Errorf("failed to decode stream frame: %w", err)
		}

		switch strings.TrimSpace(frame.Type) {
		case "botType":
			var model string
			if err := json.Unmarshal(frame.Data, &model); err != nil {
				return protocol.TextCompletionResult{}, fmt.Errorf("failed to parse botType frame: %w", err)
			}
			result.ChatModel = strings.TrimSpace(model)
			if emit != nil {
				if err := emit(protocol.TextStreamEvent{Type: frame.Type, ChatModel: result.ChatModel}); err != nil {
					return protocol.TextCompletionResult{}, err
				}
			}
		case "chunk":
			var delta string
			if err := json.Unmarshal(frame.Data, &delta); err != nil {
				return protocol.TextCompletionResult{}, fmt.Errorf("failed to parse chunk frame: %w", err)
			}
			builder.WriteString(delta)
			if emit != nil {
				if err := emit(protocol.TextStreamEvent{Type: frame.Type, Delta: delta}); err != nil {
					return protocol.TextCompletionResult{}, err
				}
			}
		case "reasoningContent":
			var delta string
			if err := json.Unmarshal(frame.Data, &delta); err != nil {
				return protocol.TextCompletionResult{}, fmt.Errorf("failed to parse reasoningContent frame: %w", err)
			}
			reasoningBuilder.WriteString(delta)
			if emit != nil {
				if err := emit(protocol.TextStreamEvent{Type: frame.Type, ReasoningContent: delta}); err != nil {
					return protocol.TextCompletionResult{}, err
				}
			}
		case "finalResult":
			var payload struct {
				MainText string `json:"mainText"`
			}
			if err := json.Unmarshal(frame.Data, &payload); err != nil {
				return protocol.TextCompletionResult{}, fmt.Errorf("failed to parse finalResult frame: %w", err)
			}
			result.Content = strings.TrimSpace(payload.MainText)
			if emit != nil {
				if err := emit(protocol.TextStreamEvent{Type: frame.Type, FinalText: result.Content}); err != nil {
					return protocol.TextCompletionResult{}, err
				}
			}
		}
	}

	if strings.TrimSpace(result.Content) == "" {
		result.Content = strings.TrimSpace(builder.String())
	}
	result.ReasoningContent = reasoningBuilder.String()
	if strings.TrimSpace(result.ChatModel) == "" {
		result.ChatModel = strings.TrimSpace(req.Model)
	}
	return result, nil
}

func textRequestTimeoutForModel(model string) time.Duration {
	if isInternetTextModel(model) {
		return textThinkingTimeout
	}
	switch strings.TrimSpace(model) {
	case "gpt-5.1", "gpt-5.2", "gpt-5.4",
		"gpt-5.1-high", "gpt-5.2-high", "gpt-5.4-high", "gpt-5.4-pro",
		"o3", "o3-pro", "o4-mini-deep-research",
		"qwen3-thinking-2507", "qwen3-max",
		"gemini-pro", "gemini-3-pro", "gemini-3.1-pro",
		"claude-3-opus", "claude-4.5-opus", "claude-4.6-opus",
		"claude-3-sonnet-high", "claude-4.6-sonnet-high":
		return textThinkingTimeout
	default:
		return textRequestTimeout
	}
}

func textStreamTimeoutForModel(model string) time.Duration {
	if isInternetTextModel(model) {
		return textStreamThinkingTimeout
	}
	switch strings.TrimSpace(model) {
	case "gpt-5.1", "gpt-5.2", "gpt-5.4",
		"gpt-5.1-high", "gpt-5.2-high", "gpt-5.4-high", "gpt-5.4-pro",
		"o3", "o3-pro", "o4-mini-deep-research",
		"qwen3-thinking-2507", "qwen3-max",
		"gemini-pro", "gemini-3-pro", "gemini-3.1-pro",
		"claude-3-opus", "claude-4.5-opus", "claude-4.6-opus",
		"claude-3-sonnet-high", "claude-4.6-sonnet-high":
		return textStreamThinkingTimeout
	default:
		return textStreamTimeout
	}
}

func isInternetTextModel(model string) bool {
	switch strings.TrimSpace(model) {
	case "gpt-4o-search-preview",
		"perplexity", "perplexity-pro",
		"gemini-2-flash-search", "gemini-3-flash-search",
		"o4-mini-deep-research":
		return true
	default:
		return false
	}
}

func (c *APIClient) pollTextJob(jobID int, jwtToken string) (protocol.TextCompletionResult, error) {
	for range textJobPollAttempts {
		time.Sleep(textJobPollInterval)

		req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/jobs/%d", chataibotAPIBaseURL, jobID), nil)
		if err != nil {
			return protocol.TextCompletionResult{}, err
		}
		applyUpstreamHeaders(req, jwtToken)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return protocol.TextCompletionResult{}, fmt.Errorf("job poll failed: %w", err)
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode >= http.StatusInternalServerError || resp.StatusCode == http.StatusNotFound {
			return protocol.TextCompletionResult{}, parseUpstreamError(resp.StatusCode, body)
		}
		if resp.StatusCode != http.StatusOK {
			continue
		}

		var job struct {
			Status string          `json:"status"`
			Data   json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(body, &job); err != nil {
			return protocol.TextCompletionResult{}, fmt.Errorf("failed to parse job response: %s", strings.TrimSpace(string(body)))
		}

		switch strings.TrimSpace(job.Status) {
		case "completed":
			result, _, err := parseDirectTextCompletion(job.Data)
			if err != nil {
				return protocol.TextCompletionResult{}, err
			}
			return result, nil
		case "error":
			return protocol.TextCompletionResult{}, fmt.Errorf("upstream job error: %s", strings.TrimSpace(string(job.Data)))
		}
	}

	return protocol.TextCompletionResult{}, fmt.Errorf("job %d did not complete in time", jobID)
}

func parseDirectTextCompletion(body []byte) (protocol.TextCompletionResult, int, error) {
	var parsed struct {
		Answer             string   `json:"answer"`
		PotentialQuestions []string `json:"potentialQuestions"`
		ChatModel          string   `json:"chatModel"`
		JobID              int      `json:"jobId"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return protocol.TextCompletionResult{}, 0, fmt.Errorf("failed to parse text completion response: %s", strings.TrimSpace(string(body)))
	}

	if parsed.JobID != 0 {
		return protocol.TextCompletionResult{}, parsed.JobID, nil
	}
	if strings.TrimSpace(parsed.Answer) == "" {
		return protocol.TextCompletionResult{}, 0, fmt.Errorf("upstream returned empty answer: %s", strings.TrimSpace(string(body)))
	}

	return protocol.TextCompletionResult{
		Content:            strings.TrimSpace(parsed.Answer),
		ChatModel:          strings.TrimSpace(parsed.ChatModel),
		PotentialQuestions: parsed.PotentialQuestions,
	}, 0, nil
}

func buildTextMessagePayload(req protocol.TextMessageRequest) map[string]any {
	return map[string]any{
		"text":                   req.Text,
		"chatId":                 req.ChatID,
		"withPotentialQuestions": req.WithPotentialQuestions,
		"model":                  req.Model,
		"isInternational":        true,
		"from":                   upstreamFromWeb,
	}
}

func (c *APIClient) newUpstreamJSONRequest(method string, url string, payload any, jwtToken string) (*http.Request, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewBuffer(encoded)
	}

	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	applyUpstreamHeaders(req, jwtToken)
	return req, nil
}

func applyUpstreamHeaders(req *http.Request, jwtToken string) {
	req.Header.Set("Cookie", "token="+jwtToken)
	req.Header.Set("x-distribution-channel", "web")
	req.Header.Set("Accept-Language", "en")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/146.0.0.0 Safari/537.36")
}

func parseUpstreamError(statusCode int, body []byte) error {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return &protocol.UpstreamError{
			StatusCode: statusCode,
			Message:    fmt.Sprintf("HTTP %d", statusCode),
		}
	}

	var payload struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	}
	if err := json.Unmarshal(body, &payload); err == nil && strings.TrimSpace(payload.Message) != "" {
		return &protocol.UpstreamError{
			StatusCode: statusCode,
			Message:    strings.TrimSpace(payload.Message),
			Type:       strings.TrimSpace(payload.Type),
		}
	}

	return &protocol.UpstreamError{
		StatusCode: statusCode,
		Message:    fmt.Sprintf("HTTP %d: %s", statusCode, trimmed),
	}
}
