package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func foreignSettings(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "settings.json")
	b := `{"model": "glm-5.3", "hooks": {"StopFailure": [{"matcher": "overloaded", "hooks": [{"type": "command", "command": "node /other/tool.js"}]}]}}`
	os.WriteFile(p, []byte(b), 0600)
	return p
}

func TestMergeHook_PreservesForeignAndIdempotent(t *testing.T) {
	p := foreignSettings(t)
	e := Entry{Event: "StopFailure", Matcher: "rate_limit", Command: "sentinel hook stop_failure"}
	if err := MergeHook(p, e); err != nil {
		t.Fatal(err)
	}
	if err := MergeHook(p, e); err != nil { // 幂等
		t.Fatal(err)
	}
	d := decode(t, p)
	sf := d["hooks"].(map[string]any)["StopFailure"].([]any)
	if len(sf) != 2 {
		t.Fatalf("want 2 entries (foreign+own), got %d", len(sf))
	}
	// 外来条目原样保留
	foreign := sf[0].(map[string]any)
	cmds := foreign["hooks"].([]any)[0].(map[string]any)
	if cmds["command"] != "node /other/tool.js" {
		t.Fatalf("foreign hook corrupted: %v", foreign)
	}
	if d["model"] != "glm-5.3" {
		t.Fatal("unrelated setting lost")
	}
}

func TestRemoveOwnHooks_OnlyOurs(t *testing.T) {
	p := foreignSettings(t)
	e := Entry{Event: "StopFailure", Matcher: "rate_limit", Command: "sentinel hook stop_failure"}
	MergeHook(p, e)
	MergeHook(p, Entry{Event: "SessionStart", Command: "sentinel hook session_start"})

	if err := RemoveOwnHooks(p, "sentinel hook"); err != nil {
		t.Fatal(err)
	}
	d := decode(t, p)
	hooks := d["hooks"].(map[string]any)
	sf := hooks["StopFailure"].([]any)
	if len(sf) != 1 || sf[0].(map[string]any)["matcher"] != "overloaded" {
		t.Fatalf("foreign entry must survive, got %v", sf)
	}
	if _, exists := hooks["SessionStart"]; exists && len(hooks["SessionStart"].([]any)) > 0 {
		t.Fatal("own SessionStart must be removed entirely")
	}
}

func decode(t *testing.T, p string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var d map[string]any
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	return d
}
