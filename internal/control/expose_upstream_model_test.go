package control

import (
	"encoding/json"
	"testing"

	"gpt-load/internal/platform/config"
	"gpt-load/internal/state"
	stateloader "gpt-load/internal/state/loader"
)

func TestExposeUpstreamModelPersistsInSystemAndGroupSettings(t *testing.T) {
	fixture := newServiceFixture(t)
	groupID := createGroupWithCredentials(t, fixture, "expose-upstream-model")

	defaults, err := fixture.service.GetGroupSettings(t.Context(), groupID)
	if err != nil || defaults.Effective.ExposeUpstreamModel {
		t.Fatalf("default effective config = %+v, %v", defaults.Effective, err)
	}
	if _, overridden := defaults.Overrides[state.SettingExposeUpstreamModel]; overridden {
		t.Fatal("default reported a group override")
	}

	got, err := fixture.service.UpdateSettings(t.Context(), SettingsUpdateRequest{Settings: map[string]json.RawMessage{
		state.SettingExposeUpstreamModel: json.RawMessage(`true`),
	}})
	if err != nil || !got.Values.ExposeUpstreamModel {
		t.Fatalf("system setting = %v, %v", got.Values.ExposeUpstreamModel, err)
	}
	group, err := fixture.service.GetGroupSettings(t.Context(), groupID)
	if err != nil || !group.Effective.ExposeUpstreamModel {
		t.Fatalf("inherited group setting = %v, %v", group.Effective.ExposeUpstreamModel, err)
	}
	if _, overridden := group.Overrides[state.SettingExposeUpstreamModel]; overridden {
		t.Fatal("inherited value reported as a group override")
	}

	group, err = fixture.service.UpdateGroupSettings(t.Context(), groupID, GroupSettingsUpdateRequest{
		Overrides: optionalField[config.Settings]{Set: true, Value: config.Settings{state.SettingExposeUpstreamModel: false}},
	})
	if err != nil || group.Effective.ExposeUpstreamModel {
		t.Fatalf("group override = %v, %v", group.Effective.ExposeUpstreamModel, err)
	}
	if value, overridden := group.Overrides[state.SettingExposeUpstreamModel]; !overridden || value != false {
		t.Fatalf("group override = %#v, want explicit false", group.Overrides)
	}

	reloaded := state.NewManager()
	if err := stateloader.New(fixture.db, reloaded, state.NewCredentialRegistry()).Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !reloaded.Current().Settings.ExposeUpstreamModel || reloaded.Current().Groups[groupID].ExposeUpstreamModel {
		t.Fatal("settings not preserved after reload")
	}

	group, err = fixture.service.UpdateGroupSettings(t.Context(), groupID, GroupSettingsUpdateRequest{
		Overrides: optionalField[config.Settings]{Set: true, Value: config.Settings{}},
	})
	if err != nil || !group.Effective.ExposeUpstreamModel {
		t.Fatal("group did not restore the inherited setting")
	}

	if _, err := fixture.service.UpdateSettings(t.Context(), SettingsUpdateRequest{Settings: map[string]json.RawMessage{
		state.SettingExposeUpstreamModel: json.RawMessage(`"true"`),
	}}); err == nil {
		t.Fatal("system accepted a non-boolean expose upstream model")
	}
	got, err = fixture.service.UpdateSettings(t.Context(), SettingsUpdateRequest{Settings: map[string]json.RawMessage{
		state.SettingExposeUpstreamModel: json.RawMessage(`null`),
	}})
	if err != nil || got.Values.ExposeUpstreamModel {
		t.Fatal("system did not restore the default")
	}
}
