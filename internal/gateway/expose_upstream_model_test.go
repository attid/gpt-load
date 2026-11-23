package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"gpt-load/internal/channel"
	"gpt-load/internal/dialect"
	"gpt-load/internal/platform/config"
	"gpt-load/internal/protocol"
	"gpt-load/internal/state"
)

var exposeUpstreamModelProtocols = []struct {
	name             string
	value            protocol.Protocol
	dialects         dialect.Set
	path             string
	requestBody      string
	upstreamResponse string
	responseField    string
}{
	{
		name: "OpenAI", value: protocol.OpenAICompletions,
		dialects: dialect.NewSet(dialect.NewOpenAI()),
		path:     "/v1/chat/completions", requestBody: `{"model":"public-model"}`,
		upstreamResponse: `{"id":"chatcmpl-1","model":"provider-response"}`,
		responseField:    "model",
	},
	{
		name: "OpenAI Responses", value: protocol.OpenAIResponses,
		dialects: dialect.NewSet(dialect.NewOpenAIResponses()),
		path:     "/v1/responses", requestBody: `{"model":"public-model","input":"ping","store":false}`,
		upstreamResponse: `{"id":"resp_1","object":"response","status":"completed","model":"provider-response","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`,
		responseField:    "model",
	},
	{
		name: "Anthropic", value: protocol.Anthropic,
		dialects: dialect.NewSet(dialect.NewAnthropic()),
		path:     "/v1/messages", requestBody: `{"model":"public-model","max_tokens":16}`,
		upstreamResponse: `{"id":"msg_1","type":"message","role":"assistant","model":"provider-response","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`,
		responseField:    "model",
	},
	{
		name: "Gemini", value: protocol.Gemini,
		dialects: dialect.NewSet(dialect.NewGemini()),
		path:     "/v1beta/models/public-model:generateContent", requestBody: `{}`,
		upstreamResponse: `{"modelVersion":"provider-response","candidates":[]}`,
		responseField:    "modelVersion",
	},
}

var exposeUpstreamModelModes = []struct {
	name   string
	system config.Settings
	group  config.Settings
	want   string
}{
	{
		name:  "group override",
		group: config.Settings{state.SettingExposeUpstreamModel: true},
		want:  "provider-response",
	},
	{
		name:   "system default",
		system: config.Settings{state.SettingExposeUpstreamModel: true},
		want:   "provider-response",
	},
	{
		name:   "group restores alias",
		system: config.Settings{state.SettingExposeUpstreamModel: true},
		group:  config.Settings{state.SettingExposeUpstreamModel: false},
		want:   "public-model",
	},
}

func TestExposeUpstreamModelKeepsProviderModelInUnaryResponses(t *testing.T) {
	for _, test := range exposeUpstreamModelProtocols {
		for _, mode := range exposeUpstreamModelModes {
			t.Run(test.name+"/"+mode.name, func(t *testing.T) {
				upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
					writer.Header().Set("Content-Type", "application/json")
					_, _ = writer.Write([]byte(test.upstreamResponse))
				}))
				defer upstream.Close()

				engine, _ := newDialectGatewayEngineWithSystemSettings(t, test.value, "public-model", test.dialects,
					newTestExecutionForwarder(t), mode.system,
					dialectGatewayGroup{
						id: 1, name: test.name, upstreamURL: upstream.URL, apiKeys: []string{"provider-key"},
						models:   []state.ModelConfig{{ID: "provider-model", Alias: "public-model"}},
						settings: mode.group,
					},
				)
				request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.requestBody))
				request.Header.Set("Authorization", "Bearer gl-client")
				recorder := httptest.NewRecorder()
				engine.ServeHTTP(recorder, request)

				if recorder.Code != http.StatusOK {
					t.Fatalf("response = %d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
				}
				var downstreamBody map[string]json.RawMessage
				if err := json.Unmarshal(recorder.Body.Bytes(), &downstreamBody); err != nil {
					t.Fatalf("decode downstream response: %v", err)
				}
				var responseModel string
				if err := json.Unmarshal(downstreamBody[test.responseField], &responseModel); err != nil || responseModel != mode.want {
					t.Fatalf("downstream model = %q, %v; want %q; body=%s", responseModel, err, mode.want, recorder.Body.String())
				}
			})
		}
	}
}

// 响应头里出现的上游模型只在隐藏别名时删除；逐协议的端到端转发另有各自的头规则。
func TestExposeUpstreamModelKeepsHeadersNamingTheProviderModel(t *testing.T) {
	source := http.Header{
		"X-Served-Model": []string{"provider-model"},
		"X-Other":        []string{"value"},
	}
	input := ForwardInput{ExternalModel: "public-model", UpstreamModelID: "provider-model"}
	if kept := sanitizeForwardResponseHeaders(source, input).Get("X-Served-Model"); kept != "" {
		t.Fatalf("hidden alias kept the provider model header: %q", kept)
	}

	input.ExposeUpstreamModel = true
	headers := sanitizeForwardResponseHeaders(source, input)
	if headers.Get("X-Served-Model") != "provider-model" || headers.Get("X-Other") != "value" {
		t.Fatalf("exposed headers = %v", headers)
	}
}

func TestExposeUpstreamModelKeepsProviderModelInStreams(t *testing.T) {
	tests := []struct {
		name        string
		value       protocol.Protocol
		dialects    dialect.Set
		path        string
		requestBody string
		streamBody  string
		want        string
	}{
		{
			name: "OpenAI", value: protocol.OpenAICompletions,
			dialects: dialect.NewSet(dialect.NewOpenAI()),
			path:     "/v1/chat/completions", requestBody: `{"model":"public-model","stream":true}`,
			streamBody: "data: {\"id\":\"1\",\"model\":\"provider-model\",\"choices\":[]}\n\ndata: [DONE]\n\n",
			want:       `"model":"provider-model"`,
		},
		{
			name: "Anthropic", value: protocol.Anthropic,
			dialects: dialect.NewSet(dialect.NewAnthropic()),
			path:     "/v1/messages", requestBody: `{"model":"public-model","max_tokens":16,"stream":true}`,
			streamBody: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"provider-model\"}}\n\n" +
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"unchanged\"}}\n\n",
			want: `"model":"provider-model"`,
		},
		{
			name: "Gemini", value: protocol.Gemini,
			dialects: dialect.NewSet(dialect.NewGemini()),
			path:     "/v1beta/models/public-model:streamGenerateContent", requestBody: `{}`,
			streamBody: "data: {\"modelVersion\":\"provider-model\",\"candidates\":[]}\n\n",
			want:       `"modelVersion":"provider-model"`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(writer, test.streamBody)
				writer.(http.Flusher).Flush()
			}))
			defer upstream.Close()

			engine, _ := newDialectGatewayEngineWithSystemSettings(t, test.value, "public-model", test.dialects,
				newTestExecutionForwarder(t), nil,
				dialectGatewayGroup{
					id: 1, name: test.name, upstreamURL: upstream.URL, apiKeys: []string{"provider-key"},
					models:   []state.ModelConfig{{ID: "provider-model", Alias: "public-model"}},
					settings: config.Settings{state.SettingExposeUpstreamModel: true},
				},
			)
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.requestBody))
			request.Header.Set("Authorization", "Bearer gl-client")
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), test.want) ||
				strings.Contains(recorder.Body.String(), "public-model") {
				t.Fatalf("stream response = %d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
			}
		})
	}
}

func TestExposeUpstreamModelKeepsProviderModelInErrorResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("X-Served-Model", "provider-model")
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = writer.Write([]byte(`{"error":"provider-model internal error"}`))
	}))
	defer upstream.Close()

	engine, _ := newDialectGatewayEngine(t, protocol.OpenAICompletions, "public-model",
		dialect.NewSet(dialect.NewOpenAI()),
		dialectGatewayGroup{id: 1, name: "openai", upstreamURL: upstream.URL, apiKeys: []string{"sk-one"},
			models:   []state.ModelConfig{{ID: "provider-model", Alias: "public-model"}},
			settings: config.Settings{state.SettingExposeUpstreamModel: true}},
	)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"public-model"}`))
	request.Header.Set("Authorization", "Bearer gl-client")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusInternalServerError ||
		!strings.Contains(recorder.Body.String(), "provider-model") ||
		strings.Contains(recorder.Body.String(), "public-model") {
		t.Fatalf("error response = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if served := recorder.Header().Get("X-Served-Model"); served != "provider-model" {
		t.Fatalf("X-Served-Model = %q, want the provider model", served)
	}
}

func TestExposeUpstreamModelAppliesToWebsocketTurns(t *testing.T) {
	requests := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var request struct {
			Model string `json:"model"`
		}
		if err := conn.ReadJSON(&request); err != nil {
			t.Error(err)
			return
		}
		requests <- request.Model
		body := fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":%q,"output":[]}}`, request.Model)
		if err := conn.WriteMessage(websocket.TextMessage, []byte(body)); err != nil {
			t.Error(err)
			return
		}
		_, _, _ = conn.ReadMessage()
	}))
	defer upstream.Close()
	handler, engine, input := websocketTestHandler(t, upstream.URL+"/v1", channel.OpenAI)
	input.Groups[0].Models = sharedAliasModels()
	input.Groups[0].Settings = config.Settings{state.SettingExposeUpstreamModel: true}
	if _, err := handler.manager.Publish(input); err != nil {
		t.Fatal(err)
	}
	sink := &recordingRequestLogSink{}
	handler.requestLogSink = sink

	server := httptest.NewServer(engine)
	defer server.Close()
	conn := dialGatewayWebsocket(t, server.URL)
	if err := conn.WriteJSON(map[string]any{"type": "response.create", "model": "public", "input": "hello", "store": false}); err != nil {
		t.Fatal(err)
	}
	_, body, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()

	served := <-requests
	if served == "" || !strings.Contains(string(body), `"model":"`+served+`"`) ||
		strings.Contains(string(body), `"model":"public"`) {
		t.Fatalf("response=%s, upstream model=%s", body, served)
	}
	events := waitWebsocketLogs(t, sink, 1)
	// 请求日志仍按客户端别名与选中模型记录，不随响应表示变化。
	if events[0].ClientModel != "public" || events[0].UpstreamModel != served {
		t.Fatalf("recorded models = %q/%q, want public/%s", events[0].ClientModel, events[0].UpstreamModel, served)
	}
}
