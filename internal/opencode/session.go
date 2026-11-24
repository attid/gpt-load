// Package opencode 负责 OpenCode 渠道的会话请求头。
//
// OpenCode 免费额度要求每个请求携带 x-opencode-session，缺少时返回
// 「MissingSessionID: OpenCode's free tier can only be used in OpenCode」。
// 客户端自带该头时原样保留；其余情况由网关按对话身份生成稳定值。
package opencode

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"gpt-load/internal/channel"
)

// Header 是 OpenCode 用于区分对话的请求头。
const Header = "x-opencode-session"

const (
	hostname         = "opencode.ai"
	maxSessionLength = 255
	// probeDomain 隔离凭据探测生成的会话值，避免与真实对话身份相撞。
	probeDomain = "gpt-load/opencode/session/v1"
)

// sessionSources 按优先级列出客户端自带的会话标识，顺序与 Bifrost 的
// HarnessSessionHeaders 一致，避免同一请求在不同组件里被当成不同会话。
var sessionSources = []string{
	"x-session-affinity",
	"x-session-id",
	"session-id",
	"session_id",
	"x-claude-code-session-id",
	"conversation_id",
}

// bodySessionSources 是按对话分组的请求体字段，比租户级连续性键更接近会话身份。
var bodySessionSources = []string{"prompt_cache_key", "previous_response_id"}

// IsTarget 与 OpenCode 客户端一致地识别目标：专用渠道，或指向 opencode.ai 的自定义地址。
// 后者覆盖把自定义渠道指向 OpenCode 的部署，与客户端判断一致。
func IsTarget(channelID channel.ID, targetConfig json.RawMessage) bool {
	switch channelID {
	case channel.OpenCodeGo, channel.OpenCodeZen:
		return true
	}
	return strings.EqualFold(baseURLHost(targetConfig), hostname)
}

// Apply 为缺失会话头的请求补上按对话推导的值。分组请求头规则在调用方随后执行，
// 因此显式配置或移除该头仍然有效。
func Apply(headers http.Header, channelID channel.ID, targetConfig json.RawMessage, body []byte, continuityKey string) {
	if headers == nil || !IsTarget(channelID, targetConfig) {
		return
	}
	if strings.TrimSpace(headers.Get(Header)) != "" {
		return
	}
	if session := Derive(headers, body, continuityKey); session != "" {
		headers.Set(Header, session)
	}
}

// ApplyForCredential 为不携带对话上下文的内部请求（凭据探测）补上会话头。
// 值由渠道、地址与凭据共同决定：同一凭据稳定，不同凭据隔离。
func ApplyForCredential(
	headers http.Header,
	channelID channel.ID,
	targetConfig json.RawMessage,
	credentialID uint,
) {
	if headers == nil || !IsTarget(channelID, targetConfig) {
		return
	}
	if strings.TrimSpace(headers.Get(Header)) != "" {
		return
	}
	material := strings.Join([]string{
		probeDomain,
		string(channelID),
		baseURL(targetConfig),
		strconv.FormatUint(uint64(credentialID), 10),
	}, "\x00")
	headers.Set(Header, digest(material))
}

// Derive 选择最接近「对话」的稳定标识：客户端会话头、请求体的缓存或续接标识，
// 最后是本次请求的租户级连续性键。都没有时返回空。
func Derive(headers http.Header, body []byte, continuityKey string) string {
	for _, name := range sessionSources {
		if session := Normalize(headers.Get(name)); session != "" {
			return session
		}
	}
	for _, field := range bodySessionSources {
		if session := Normalize(bodyString(body, field)); session != "" {
			return session
		}
	}
	return Normalize(continuityKey)
}

// Normalize 保留客户端可接受的标识原样，其余值折叠为定长摘要，
// 避免把超长、含空白或非 ASCII 的值发往上游。
func Normalize(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if len(trimmed) <= maxSessionLength && valid(trimmed) {
		return trimmed
	}
	return digest(trimmed)
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// valid 对齐 OpenCode 客户端的会话标识格式：字母数字开头结尾，中间允许 . _ -。
func valid(value string) bool {
	for index, character := range value {
		alphanumeric := character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9'
		if index == 0 || index == len(value)-1 {
			if !alphanumeric {
				return false
			}
			continue
		}
		if !alphanumeric && character != '.' && character != '_' && character != '-' {
			return false
		}
	}
	return true
}

func bodyString(body []byte, field string) string {
	if len(body) == 0 {
		return ""
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil {
		return ""
	}
	raw, exists := object[field]
	if !exists {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return value
}

func baseURL(targetConfig json.RawMessage) string {
	if len(targetConfig) == 0 {
		return ""
	}
	var config struct {
		BaseURL string `json:"base_url"`
	}
	if err := json.Unmarshal(targetConfig, &config); err != nil {
		return ""
	}
	return strings.TrimSpace(config.BaseURL)
}

func baseURLHost(targetConfig json.RawMessage) string {
	raw := baseURL(targetConfig)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}
