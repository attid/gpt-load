package cpa

import (
	"strings"
	"testing"
	"time"

	"gpt-load/internal/execution"
	"gpt-load/internal/protocol"
	"gpt-load/internal/subscription/providers/codex"
)

func TestExposedUpstreamModelSkipsStreamAliasRewrite(t *testing.T) {
	spec := execution.AttemptSpec{
		ClientProtocol:      protocol.OpenAICompletions,
		ClientModel:         "public-model",
		UpstreamModel:       "upstream-model",
		ExposeUpstreamModel: true,
	}
	payload := []byte("data: {\"model\":\"reported-model\"}\n\n")
	rewritten, err := rewriteStreamModelAlias(spec, payload)
	if err != nil || string(rewritten) != string(payload) {
		t.Fatalf("exposed rewrite = %q, %v; want unchanged", rewritten, err)
	}

	spec.ExposeUpstreamModel = false
	rewritten, err = rewriteStreamModelAlias(spec, payload)
	if err != nil || !strings.Contains(string(rewritten), `"model":"public-model"`) {
		t.Fatalf("alias rewrite = %q, %v", rewritten, err)
	}
}

func TestAdapterKeepsReportedModelWhenUpstreamModelIsExposed(t *testing.T) {
	tests := []struct {
		name      string
		protocol  protocol.Protocol
		operation execution.Operation
		payload   string
	}{
		{name: "OpenAI Chat", protocol: protocol.OpenAICompletions, operation: execution.OperationChatCompletion, payload: `{"model":"reported-model","choices":[{"finish_reason":"stop"}]}`},
		{name: "OpenAI Responses", protocol: protocol.OpenAIResponses, operation: execution.OperationResponsesCreate, payload: "data: {\"type\":\"response.completed\",\"response\":{\"model\":\"reported-model\"}}\n\n"},
		{name: "Anthropic", protocol: protocol.Anthropic, operation: execution.OperationChatCompletion, payload: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"reported-model\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}"},
		{name: "Gemini", protocol: protocol.Gemini, operation: execution.OperationChatCompletion, payload: `{"modelVersion":"reported-model","candidates":[{"finishReason":"STOP"}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter, _, _, keyService, row := newAdapterFixture(t, credentialJSON("access", "refresh", time.Now().Add(time.Hour)))
			chunks := make(chan codex.ExecuteStreamChunk, 1)
			chunks <- codex.ExecuteStreamChunk{Payload: []byte(test.payload)}
			close(chunks)
			setCodexExecutor(t, adapter, &fakeExecutor{stream: &codex.ExecuteStreamResponse{Chunks: chunks}})
			spec := validSpec(t, row, keyService)
			spec.ClientProtocol = test.protocol
			spec.Operation = test.operation
			spec.ClientModel = "public-model"
			spec.UpstreamModel = "upstream-model"
			spec.ExposeUpstreamModel = true
			if test.protocol != protocol.OpenAIResponses {
				spec.RouteMode = execution.RouteConverted
			}

			var wire strings.Builder
			result := adapter.ExecuteStream(t.Context(), spec, func(event execution.StreamEvent) error {
				if event.Kind == execution.StreamEventData {
					_, _ = wire.Write(event.Data)
				}
				return nil
			})
			if result.Error != nil || result.Model != "reported-model" ||
				!strings.Contains(wire.String(), "reported-model") || strings.Contains(wire.String(), "public-model") {
				t.Fatalf("result = %#v, wire = %q", result, wire.String())
			}
		})
	}
}
