package daemon

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Golden-forest/claude-sentinel/internal/binding"
)

func setupTestPane(t *testing.T) (socket, pane, logFile string) {
	t.Helper()
	socket = "testsent-daemon"
	_ = exec.Command("tmux", "-S", socket, "kill-server").Run()
	logFile = filepath.Join(t.TempDir(), "keys.log")
	run := func(args ...string) {
		out, err := exec.Command("tmux", append([]string{"-S", socket}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("tmux %v: %v %s", args, err, out)
		}
	}
	run("new-session", "-d", "-s", "t", "cat > "+logFile)
	time.Sleep(300 * time.Millisecond)
	out, _ := exec.Command("tmux", "-S", socket, "list-panes", "-t", "t", "-F", "#{pane_id}").Output()
	t.Cleanup(func() { exec.Command("tmux", "-S", socket, "kill-server").Run() })
	return socket, string(out[:len(out)-1]), logFile
}

func postEvent(t *testing.T, sockPath string, ev map[string]any) {
	t.Helper()
	c, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	b, _ := json.Marshal(ev)
	c.Write(append(b, '\n'))
}

// 端到端：session_start 建绑定 → stop_failure 造事故 → 重置到期 → 敲键到达 pane
func TestDaemon_EndToEnd(t *testing.T) {
	dir := t.TempDir()
	tmuxSocket, pane, logFile := setupTestPane(t)

	sleep := exec.Command("sleep", "30")
	sleep.Start()
	t.Cleanup(func() { sleep.Process.Kill() })
	started, err := binding.ProcStartTime(sleep.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		SocketPath: filepath.Join(dir, "bus.sock"),
		SpoolDir:   filepath.Join(dir, "spool"),
		StatePath:  filepath.Join(dir, "state.json"),
		TmuxSocket: tmuxSocket,
		RetryText:  "continue",
		Tick:       100 * time.Millisecond,
	}
	os.MkdirAll(cfg.SpoolDir, 0700)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Run(ctx, cfg)

	// 等 socket 就绪
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.Dial("unix", cfg.SocketPath); err == nil {
			c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// 1) 绑定
	postEvent(t, cfg.SocketPath, map[string]any{
		"id": "e1", "kind": "session_start", "session_id": "s-e2e", "ts": time.Now().Unix(),
		"payload": map[string]any{
			"tmux_socket": tmuxSocket, "pane_id": pane,
			"pid": sleep.Process.Pid, "started_at": started,
		},
	})
	// 2) 限额事故（重置时刻 = 现在 + 2 秒）
	resetAt := time.Now().Add(2 * time.Second).Format("2006-01-02 15:04:05")
	postEvent(t, cfg.SocketPath, map[string]any{
		"id": "e2", "kind": "stop_failure", "session_id": "s-e2e", "ts": time.Now().Unix(),
		"payload": map[string]any{
			"error":                  "rate_limit",
			"last_assistant_message": "API Error: Request rejected (429) · 已达到 5 小时的使用上限。您的限额将在 " + resetAt + " 重置。",
		},
	})

	// 3) 等敲键落盘（重置 2s + 裕量）
	got := ""
	for i := 0; i < 100; i++ {
		time.Sleep(100 * time.Millisecond)
		if b, err := os.ReadFile(logFile); err == nil && len(b) > 0 {
			got = string(b)
			break
		}
	}
	if got != "continue\n" {
		t.Fatalf("pane must receive exactly one continue, got %q", got)
	}
	_ = strconv.Itoa // 保持导入
}
