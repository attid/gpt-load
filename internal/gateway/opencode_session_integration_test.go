package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gpt-load/internal/channel"
	"gpt-load/internal/dialect"
	"gpt-load/internal/opencode"
	"gpt-load/internal/protocol"
	"gpt-load/internal/state"
)

func openCodeSessionChatFixture(t *testing.T, sessions chan<- string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Error(err)
		}
		sessions <- r.Header.Get(opencode.Header)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chat_1","object":"chat.completion","created":1,"model":"upstream-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(server.Close)
	return server
}

func serveChatCompletion(t *testing.T, engine http.Handler, body string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer gl-client")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("response = %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestOpenCodeSessionHeaderIsGeneratedForOpenCodeChannels(t *testing.T) {
	sessions := make(chan string, 4)
	upstream := openCodeSessionChatFixture(t, sessions)
	params, err := json.Marshal(map[string]string{"base_url": upstream.URL + "/zen/go"})
	if err != nil {
		t.Fatal(err)
	}
	engine, _ := newDialectGatewayEngine(t, protocol.OpenAICompletions, "upstream-model",
		dialect.NewSet(dialect.NewOpenAI()),
		dialectGatewayGroup{id: 1, name: "opencode-go", channelID: channel.OpenCodeGo, params: params,
			apiKeys: []string{"provider-key"},
			models:  []state.ModelConfig{{ID: "upstream-model"}}},
	)

	body := `{"model":"upstream-model","messages":[{"role":"system","content":"same prefix"},{"role":"user","content":"hello"}]}`
	serveChatCompletion(t, engine, body)
	serveChatCompletion(t, engine, body)

	first, second := <-sessions, <-sessions
	for index, session := range []string{first, second} {
		if len(session) != 64 || len(opencode.Normalize(session)) != len(session) {
			t.Fatalf("request %d session header = %q, want a 64-character digest", index, session)
		}
	}
	if first != second {
		t.Fatalf("session header is not stable for the same conversation: %q != %q", first, second)
	}
}

func TestOpenCodeSessionHeaderGroupRuleWins(t *testing.T) {
	sessions := make(chan string, 2)
	upstream := openCodeSessionChatFixture(t, sessions)
	params, err := json.Marshal(map[string]string{"base_url": upstream.URL + "/zen/go"})
	if err != nil {
		t.Fatal(err)
	}
	engine, _ := newDialectGatewayEngine(t, protocol.OpenAICompletions, "upstream-model",
		dialect.NewSet(dialect.NewOpenAI()),
		dialectGatewayGroup{id: 1, name: "opencode-go", channelID: channel.OpenCodeGo, params: params,
			apiKeys:     []string{"provider-key"},
			models:      []state.ModelConfig{{ID: "upstream-model"}},
			headerRules: state.HeaderRules{Set: map[string]string{opencode.Header: "fixed-session"}}},
	)

	serveChatCompletion(t, engine, `{"model":"upstream-model","messages":[{"role":"user","content":"hello"}]}`)
	if got := <-sessions; got != "fixed-session" {
		t.Fatalf("session header = %q, want the group rule value", got)
	}
}

func TestOpenCodeSessionHeaderSkipsOtherChannels(t *testing.T) {
	sessions := make(chan string, 1)
	upstream := openCodeSessionChatFixture(t, sessions)
	engine, _ := newDialectGatewayEngine(t, protocol.OpenAICompletions, "upstream-model",
		dialect.NewSet(dialect.NewOpenAI()),
		dialectGatewayGroup{id: 1, name: "openai", upstreamURL: upstream.URL, apiKeys: []string{"provider-key"},
			models: []state.ModelConfig{{ID: "upstream-model"}}},
	)

	serveChatCompletion(t, engine, `{"model":"upstream-model","messages":[{"role":"user","content":"hello"}]}`)
	if got := <-sessions; got != "" {
		t.Fatalf("session header = %q, want none for a non-OpenCode channel", got)
	}
}
