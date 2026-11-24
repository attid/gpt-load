package opencode

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"gpt-load/internal/channel"
)

func targetConfig(t *testing.T, baseURL string) json.RawMessage {
	t.Helper()
	if baseURL == "" {
		return nil
	}
	encoded, err := json.Marshal(map[string]string{"base_url": baseURL})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestApplyKeepsClientValue(t *testing.T) {
	headers := http.Header{}
	headers.Set(Header, "client-conversation")
	Apply(headers, channel.OpenCodeGo, nil, []byte(`{}`), strings.Repeat("a1", 32))
	if got := headers.Get(Header); got != "client-conversation" {
		t.Fatalf("session header = %q, want the client value", got)
	}
}

func TestIsTargetMatchesOpenCodeChannelsAndHosts(t *testing.T) {
	for _, test := range []struct {
		name      string
		channelID channel.ID
		baseURL   string
		want      bool
	}{
		{name: "go channel", channelID: channel.OpenCodeGo, want: true},
		{name: "zen channel", channelID: channel.OpenCodeZen, want: true},
		{name: "custom base url", channelID: channel.OpenAICompatible, baseURL: "https://opencode.ai/zen/go", want: true},
		{name: "uppercase host", channelID: channel.OpenAICompatible, baseURL: "https://OpenCode.AI/zen", want: true},
		{name: "other channel", channelID: channel.OpenAI, want: false},
		{name: "other host", channelID: channel.OpenAICompatible, baseURL: "https://api.example.com/v1", want: false},
		{name: "subdomain is a different host", channelID: channel.OpenAICompatible, baseURL: "https://api.opencode.ai/v1", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := IsTarget(test.channelID, targetConfig(t, test.baseURL)); got != test.want {
				t.Fatalf("IsTarget = %v, want %v", got, test.want)
			}
		})
	}
}

func TestDerivePrefersConversationIdentity(t *testing.T) {
	continuity := strings.Repeat("a1", 32)
	for _, test := range []struct {
		name    string
		headers map[string]string
		body    string
		want    string
	}{
		{
			name:    "client session affinity header",
			headers: map[string]string{"x-session-affinity": "conv-affinity", "x-session-id": "conv-id"},
			body:    `{"prompt_cache_key":"cache-key"}`,
			want:    "conv-affinity",
		},
		{
			name:    "client session id header",
			headers: map[string]string{"x-session-id": "conv-id"},
			body:    `{"prompt_cache_key":"cache-key"}`,
			want:    "conv-id",
		},
		{name: "prompt cache key", body: `{"prompt_cache_key":"cache-key"}`, want: "cache-key"},
		{name: "previous response id", body: `{"previous_response_id":"resp_123"}`, want: "resp_123"},
		{name: "continuity key fallback", body: `{}`, want: continuity},
		{name: "nothing to derive", body: `{}`, want: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			headers := http.Header{}
			for name, value := range test.headers {
				headers.Set(name, value)
			}
			key := continuity
			if test.name == "nothing to derive" {
				key = ""
			}
			if got := Derive(headers, []byte(test.body), key); got != test.want {
				t.Fatalf("Derive = %q, want %q", got, test.want)
			}
		})
	}
}

func TestNormalizeFoldsUnusableValues(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		want  string
	}{
		{name: "uuid", value: "0192f3a4-1111-7abc-8def-0123456789ab", want: "0192f3a4-1111-7abc-8def-0123456789ab"},
		{name: "underscores and dots", value: "session_1.2", want: "session_1.2"},
		{name: "surrounding whitespace", value: "  spaced value  ", want: ""},
		{name: "inner whitespace", value: "two words", want: ""},
		{name: "non ascii", value: "сессия", want: ""},
		{name: "too long", value: strings.Repeat("x", maxSessionLength+1), want: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := Normalize(test.value)
			if test.want == "" {
				if len(got) != 64 || !valid(got) {
					t.Fatalf("Normalize(%q) = %q, want a 64-character digest", test.value, got)
				}
				return
			}
			if got != test.want {
				t.Fatalf("Normalize(%q) = %q, want %q", test.value, got, test.want)
			}
		})
	}
	if got := Normalize("   "); got != "" {
		t.Fatalf("blank session = %q, want empty", got)
	}
}

func TestApplyForCredentialIsStableAndPerCredential(t *testing.T) {
	first := http.Header{}
	ApplyForCredential(first, channel.OpenCodeGo, nil, 7)
	again := http.Header{}
	ApplyForCredential(again, channel.OpenCodeGo, nil, 7)
	other := http.Header{}
	ApplyForCredential(other, channel.OpenCodeGo, nil, 8)

	value := first.Get(Header)
	if len(value) != 64 || !valid(value) {
		t.Fatalf("credential session = %q, want a 64-character digest", value)
	}
	if again.Get(Header) != value {
		t.Fatal("credential session is not stable")
	}
	if other.Get(Header) == value {
		t.Fatal("different credentials share a session")
	}
	if !IsTarget(channel.OpenCodeGo, nil) {
		t.Fatal("opencode channel is not a target")
	}
	unchanged := http.Header{}
	unchanged.Set(Header, "fixed")
	ApplyForCredential(unchanged, channel.OpenCodeGo, nil, 7)
	if got := unchanged.Get(Header); got != "fixed" {
		t.Fatalf("existing session overwritten: %q", got)
	}
	skipped := http.Header{}
	ApplyForCredential(skipped, channel.OpenAI, nil, 7)
	if got := skipped.Get(Header); got != "" {
		t.Fatalf("non-OpenCode channel received session header %q", got)
	}
}
