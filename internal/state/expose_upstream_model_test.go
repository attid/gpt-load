package state

import (
	"encoding/json"
	"testing"

	"gpt-load/internal/channel"
	"gpt-load/internal/platform/config"
)

func TestExposeUpstreamModelIsAPublicSwitchWithGroupOverride(t *testing.T) {
	if !IsRuntimeSettingKey(SettingExposeUpstreamModel) {
		t.Fatalf("IsRuntimeSettingKey(%q) = false", SettingExposeUpstreamModel)
	}
	for _, value := range []any{true, false} {
		if err := ValidateRuntimeSetting(SettingExposeUpstreamModel, value); err != nil {
			t.Fatalf("ValidateRuntimeSetting(%v) error = %v", value, err)
		}
	}
	for _, value := range []any{"true", json.Number("1"), nil} {
		if err := ValidateRuntimeSetting(SettingExposeUpstreamModel, value); err == nil {
			t.Fatalf("ValidateRuntimeSetting(%v) accepted a non-boolean", value)
		}
	}
	if DefaultRuntimeSettings().ExposeUpstreamModel {
		t.Fatal("default expose upstream model = true, want false")
	}
	system, err := ResolveRuntimeSettings(config.Settings{SettingExposeUpstreamModel: true})
	if err != nil || !system.ExposeUpstreamModel {
		t.Fatalf("system setting = %#v, %v; want true", system, err)
	}
	inherited, err := ResolveGroupRuntimeSettings(system, nil)
	if err != nil || !inherited.ExposeUpstreamModel {
		t.Fatalf("inherited group setting = %#v, %v; want true", inherited, err)
	}
	overridden, err := ResolveGroupRuntimeSettings(system, config.Settings{SettingExposeUpstreamModel: false})
	if err != nil || overridden.ExposeUpstreamModel {
		t.Fatalf("overridden group setting = %#v, %v; want false", overridden, err)
	}
	if _, err := ResolveGroupRuntimeSettings(system, config.Settings{SettingExposeUpstreamModel: "true"}); err == nil {
		t.Fatal("group accepted a non-boolean expose upstream model")
	}
}

func TestCompilePublishesExposedUpstreamModelPerGroup(t *testing.T) {
	params := json.RawMessage(`{"base_url":"https://api.example.com/v1"}`)
	snapshot, err := Compile(CompileInput{
		ChannelRegistry: channel.NewRegistry(),
		SystemSettings:  config.Settings{SettingExposeUpstreamModel: true},
		Groups: []GroupConfig{
			{ConnectionType: "api_key", ID: 1, ChannelID: channel.OpenAI, Params: params, Enabled: true},
			{ConnectionType: "api_key", ID: 2, ChannelID: channel.OpenAI, Params: params, Enabled: true,
				Settings: config.Settings{SettingExposeUpstreamModel: false}},
		},
	})
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	if !snapshot.Settings.ExposeUpstreamModel {
		t.Fatal("system expose upstream model = false")
	}
	if !snapshot.Groups[1].ExposeUpstreamModel {
		t.Fatal("group 1 did not inherit the exposed upstream model")
	}
	if snapshot.Groups[2].ExposeUpstreamModel {
		t.Fatal("group 2 did not restore the client model alias")
	}
}
