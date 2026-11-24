package bifrost

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gpt-load/internal/execution"
	"gpt-load/internal/protocol"
)

func TestNativeAliasRewriteDecisionHonorsExposedUpstreamModel(t *testing.T) {
	t.Parallel()

	spec := execution.AttemptSpec{
		ClientProtocol: protocol.OpenAICompletions,
		ClientModel:    "public-model",
		UpstreamModel:  "provider-model",
	}
	if !needsClientModelAlias(spec) || newNativeAliasSSERewriter(spec) == nil {
		t.Fatal("alias rewrite disabled without the exposed upstream model")
	}
	spec.ExposeUpstreamModel = true
	if needsClientModelAlias(spec) || newNativeAliasSSERewriter(spec) != nil {
		t.Fatal("alias rewrite enabled with the exposed upstream model")
	}
}

func TestExposedUpstreamModelSkipsNativeAliasRewrite(t *testing.T) {
	t.Parallel()

	t.Run("unary JSON", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, `{"id":"chat_1","object":"chat.completion","created":1,"model":"provider-model","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
		}))
		defer server.Close()

		runtime := newTestRuntime(t)
		spec := compatibleSpec(server.URL)
		spec.ClientModel = "public-model"
		spec.UpstreamModel = "requested-model"
		spec.ExposeUpstreamModel = true
		result := runtime.Execute(context.Background(), spec)
		if err := result.Validate(); err != nil {
			t.Fatalf("result validation: %v; result=%+v", err, result)
		}
		if result.Model != "provider-model" ||
			!bytes.Contains(result.Body, []byte(`"model":"provider-model"`)) ||
			bytes.Contains(result.Body, []byte("public-model")) {
			t.Fatalf("model/body = %q/%s", result.Model, result.Body)
		}
	})

	t.Run("SSE split across upstream writes", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "text/event-stream")
			flusher := writer.(http.Flusher)
			_, _ = io.WriteString(writer, `data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"provider-`)
			flusher.Flush()
			time.Sleep(5 * time.Millisecond)
			_, _ = io.WriteString(writer, "model\",\"choices\":[]}\n\ndata: [DONE]\n\n")
			flusher.Flush()
		}))
		defer server.Close()

		runtime := newTestRuntime(t)
		spec := compatibleSpec(server.URL)
		spec.ClientModel = "public-model"
		spec.UpstreamModel = "requested-model"
		spec.ExposeUpstreamModel = true
		var data bytes.Buffer
		result := runtime.ExecuteStream(context.Background(), spec, func(event execution.StreamEvent) error {
			if event.Kind == execution.StreamEventData {
				data.Write(event.Data)
			}
			return nil
		})
		if err := result.Validate(); err != nil {
			t.Fatalf("result validation: %v; result=%+v", err, result)
		}
		if result.Model != "provider-model" ||
			!bytes.Contains(data.Bytes(), []byte(`"model":"provider-model"`)) ||
			bytes.Contains(data.Bytes(), []byte("public-model")) {
			t.Fatalf("model/data = %q/%s", result.Model, data.String())
		}
	})
}

func TestExposedUpstreamModelKeepsClineChatModelInConvertedResponses(t *testing.T) {
	t.Parallel()

	const upstreamModel = "deepseek/deepseek-v4.1-flash"

	t.Run("unary", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, `{"success":true,"data":`+clineCompletionFixture+`}`)
		}))
		defer server.Close()

		runtime := newRuntimeForTest(t, testRuntimeOptions{allowPrivateNetwork: true})
		spec := clineTestSpec(t, server.URL, protocol.OpenAIResponses)
		spec.ExposeUpstreamModel = true
		result := runtime.Execute(t.Context(), spec)
		if result.Error != nil || result.StatusCode != http.StatusOK {
			t.Fatalf("Cline result = %+v", result)
		}
		if !strings.Contains(string(result.Body), `"model":"`+upstreamModel+`"`) ||
			strings.Contains(string(result.Body), "client-model") {
			t.Fatalf("converted Cline response = %s", result.Body)
		}
	})

	t.Run("stream", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(writer, `data: {"id":"c","model":"`+upstreamModel+`","choices":[{"index":0,"delta":{"role":"assistant","content":"first token"}}]}`+"\n\n")
			writer.(http.Flusher).Flush()
			// 第二个分片不携带 model：应回退到本次选中的上游模型，而不是客户端别名。
			_, _ = io.WriteString(writer, "data: {\"id\":\"c\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		}))
		defer server.Close()

		runtime := newRuntimeForTest(t, testRuntimeOptions{allowPrivateNetwork: true})
		spec := clineTestSpec(t, server.URL, protocol.OpenAIResponses)
		spec.ExposeUpstreamModel = true
		var wire strings.Builder
		result := runtime.ExecuteStream(t.Context(), spec, func(event execution.StreamEvent) error {
			if event.Kind == execution.StreamEventData {
				_, _ = wire.Write(event.Data)
			}
			return nil
		})
		if result.Error != nil {
			t.Fatalf("Cline stream result = %+v", result)
		}
		if !strings.Contains(wire.String(), `"model":"`+upstreamModel+`"`) ||
			strings.Contains(wire.String(), "client-model") {
			t.Fatalf("converted Cline stream = %q", wire.String())
		}
	})
}
