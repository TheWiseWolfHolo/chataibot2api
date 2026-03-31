package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"chataibot2api/protocol"
)

type rewriteHostTransport struct {
	base   http.RoundTripper
	target *url.URL
}

func (t rewriteHostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	rewritten := *cloned.URL
	rewritten.Scheme = t.target.Scheme
	rewritten.Host = t.target.Host
	cloned.URL = &rewritten
	return t.base.RoundTrip(cloned)
}

func TestStreamTextMessageParsesReasoningContentFrames(t *testing.T) {
	t.Helper()

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/message/streaming" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `{"type":"botType","data":"gpt-5.4"}{"type":"reasoningContent","data":"thinking step 1"}{"type":"chunk","data":"final answer"}{"type":"finalResult","data":{"mainText":"final answer"}}`)
	}))
	defer server.Close()

	targetURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse test server url: %v", err)
	}

	testHTTPClient := server.Client()
	testHTTPClient.Transport = rewriteHostTransport{
		base:   testHTTPClient.Transport,
		target: targetURL,
	}

	client := NewAPIClient()
	client.httpClient = testHTTPClient

	var events []protocol.TextStreamEvent
	resp, err := client.StreamTextMessage(protocol.TextMessageRequest{
		Text:   "probe",
		ChatID: 42,
		Model:  "gpt-5.4",
	}, "jwt-token", func(event protocol.TextStreamEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("expected stream to parse, got %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("expected 4 emitted events, got %+v", events)
	}
	if events[1].Type != "reasoningContent" || events[1].ReasoningContent != "thinking step 1" {
		t.Fatalf("expected reasoningContent event, got %+v", events[1])
	}
	if resp.ReasoningContent != "thinking step 1" {
		t.Fatalf("expected accumulated reasoning content, got %+v", resp)
	}
	if resp.Content != "final answer" {
		t.Fatalf("expected final answer to survive reasoning frames, got %+v", resp)
	}
}

func TestTextRequestTimeoutForModelGivesThinkingModelsMoreTime(t *testing.T) {
	t.Helper()

	regular := textRequestTimeoutForModel("gpt-4.1-nano")
	reasoning := textRequestTimeoutForModel("gpt-5.4")
	qwenThinking := textRequestTimeoutForModel("qwen3-thinking-2507")

	if regular < 15*time.Second {
		t.Fatalf("expected regular text timeout to be relaxed above legacy 10s, got %s", regular)
	}
	if reasoning <= regular {
		t.Fatalf("expected reasoning model timeout to exceed regular timeout, got regular=%s reasoning=%s", regular, reasoning)
	}
	if qwenThinking <= regular {
		t.Fatalf("expected qwen thinking timeout to exceed regular timeout, got regular=%s qwen=%s", regular, qwenThinking)
	}
}

func TestTextTimeoutsForInternetModelsExceedRegularModels(t *testing.T) {
	t.Helper()

	regularRequest := textRequestTimeoutForModel("gpt-4.1-nano")
	internetRequest := textRequestTimeoutForModel("gpt-4o-search-preview")
	regularStream := textStreamTimeoutForModel("gpt-4.1-nano")
	internetStream := textStreamTimeoutForModel("gpt-4o-search-preview")

	if internetRequest <= regularRequest {
		t.Fatalf("expected internet text request timeout to exceed regular timeout, got regular=%s internet=%s", regularRequest, internetRequest)
	}
	if internetStream <= regularStream {
		t.Fatalf("expected internet text stream timeout to exceed regular stream timeout, got regular=%s internet=%s", regularStream, internetStream)
	}
}

func TestTextStreamTimeoutsAllowLongRunningStreams(t *testing.T) {
	t.Helper()

	regular := textStreamTimeoutForModel("gpt-4.1-nano")
	internet := textStreamTimeoutForModel("gpt-4o-search-preview")
	reasoning := textStreamTimeoutForModel("gpt-5.4")

	if regular < 45*time.Second {
		t.Fatalf("expected regular stream timeout to comfortably exceed short SSE responses, got %s", regular)
	}
	if internet < 90*time.Second {
		t.Fatalf("expected internet/search stream timeout to allow slow first token and long fetches, got %s", internet)
	}
	if reasoning < 90*time.Second {
		t.Fatalf("expected reasoning stream timeout to allow long-running generations, got %s", reasoning)
	}
}
