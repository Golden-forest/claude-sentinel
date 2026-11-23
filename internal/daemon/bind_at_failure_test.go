package daemon

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Golden-forest/claude-sentinel/internal/binding"
	"github.com/Golden-forest/claude-sentinel/internal/eventbus"
	"github.com/Golden-forest/claude-sentinel/internal/machine"
)

// F1：StopFailure 事件自带 tmux 环境时当场建绑定——覆盖「sentinel 安装前
// 已在跑的会话」（它们不会再发 session_start，没有这条路径就永远 NEEDS_REVIEW）。
func TestF1_BindAtFailureTime(t *testing.T) {
	store, err := machine.OpenStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	bindings := binding.NewTable()

	sleep := exec.Command("sleep", "30")
	if err := sleep.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sleep.Process.Kill() })
	started, err := binding.ProcStartTime(sleep.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}

	resetAt := time.Now().Add(time.Hour).Format("2006-01-02 15:04:05")
	payload, _ := json.Marshal(map[string]any{
		"error":                  "rate_limit",
		"last_assistant_message": "API Error: Request rejected (429) · 已达到 5 小时的使用上限。您的限额将在 " + resetAt + " 重置。",
		"tmux_socket":            "/tmp/tmux-501/default",
		"pane_id":                "%9",
		"pid":                    sleep.Process.Pid,
		"started_at":             started,
	})
	handleEvent(store, bindings, eventbus.Event{
		ID: "f1", Kind: "stop_failure", SessionID: "s-f1",
		Payload: payload, Ts: time.Now().Unix(),
	})

	b, ok := bindings.Get("s-f1")
	if !ok || b.PaneID != "%9" || b.Pid != sleep.Process.Pid || b.TmuxSocket != "/tmp/tmux-501/default" {
		t.Fatalf("stop_failure must bind session from payload env fields: %+v ok=%v", b, ok)
	}
}
