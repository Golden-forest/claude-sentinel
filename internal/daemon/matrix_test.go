package daemon

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Golden-forest/claude-sentinel/internal/binding"
)

// 多会话安全：同 tmux server 三个 pane，只绑定 A；A 的 3 个事故全部只落在 A 的 pane。
func TestMatrix_MultiPaneSafety(t *testing.T) {
	dir := t.TempDir()
	socket := "testsent-matrix"
	_ = exec.Command("tmux", "-S", socket, "kill-server").Run()
	run := func(args ...string) string {
		out, err := exec.Command("tmux", append([]string{"-S", socket}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("tmux %v: %v %s", args, err, out)
		}
		return string(out)
	}
	logs := make([]string, 3)
	panes := make([]string, 3)
	for i := 0; i < 3; i++ {
		logs[i] = filepath.Join(dir, "log"+string(rune('A'+i)))
		run("new-session", "-d", "-s", "t"+string(rune('A'+i)), "cat > "+logs[i])
		time.Sleep(150 * time.Millisecond)
		out := run("list-panes", "-t", "t"+string(rune('A'+i)), "-F", "#{pane_id}")
		panes[i] = trimLastNewline(out)
	}
	t.Cleanup(func() { exec.Command("tmux", "-S", socket, "kill-server").Run() })

	sleep := exec.Command("sleep", "60")
	sleep.Start()
	t.Cleanup(func() { sleep.Process.Kill() })
	started, _ := binding.ProcStartTime(sleep.Process.Pid)

	cfg := Config{
		SocketPath: filepath.Join(dir, "bus.sock"),
		SpoolDir:   filepath.Join(dir, "spool"),
		StatePath:  filepath.Join(dir, "state.json"),
		RetryText:  "continue",
		Tick:       100 * time.Millisecond,
	}
	os.MkdirAll(cfg.SpoolDir, 0700)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Run(ctx, cfg)

	waitSocket(t, cfg.SocketPath)
	post := func(m map[string]any) {
		c, err := net.Dial("unix", cfg.SocketPath)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		b, _ := json.Marshal(m)
		c.Write(append(b, '\n'))
	}

	// 只绑定 pane A（session s-A）
	post(map[string]any{"id": "m0", "kind": "session_start", "session_id": "s-A", "ts": 1,
		"payload": map[string]any{"tmux_socket": socket, "pane_id": panes[0], "pid": sleep.Process.Pid, "started_at": started}})

	// 3 个事故（重置错开 1s）
	for i := 0; i < 3; i++ {
		reset := time.Now().Add(time.Duration(1+i) * time.Second).Format("2006-01-02 15:04:05")
		post(map[string]any{"id": "mf" + string(rune('0'+i)), "kind": "stop_failure", "session_id": "s-A", "ts": 2,
			"payload": map[string]any{"error": "rate_limit",
				"last_assistant_message": "已达到 5 小时的使用上限。您的限额将在 " + reset + " 重置。"}})
		time.Sleep(50 * time.Millisecond)
	}

	// 等 6s：三个重置全部到期并执行
	time.Sleep(6 * time.Second)

	a := readFileOrEmpty(t, logs[0])
	b := readFileOrEmpty(t, logs[1])
	c := readFileOrEmpty(t, logs[2])
	if a != "continue\ncontinue\ncontinue\n" {
		t.Fatalf("pane A must receive exactly 3 continues, got %q", a)
	}
	if b != "" || c != "" {
		t.Fatalf("unbound panes must stay untouched: B=%q C=%q", b, c)
	}
}

// 双源去重：同一事故（同 session 同重置时刻）投两次 → 只有一个 incident、只发一次
func TestMatrix_DualSourceDedup(t *testing.T) {
	dir := t.TempDir()
	socket := "testsent-dedup"
	_ = exec.Command("tmux", "-S", socket, "kill-server").Run()
	logF := filepath.Join(dir, "log")
	exec.Command("tmux", "-S", socket, "new-session", "-d", "-s", "t", "cat > "+logF).Run()
	time.Sleep(250 * time.Millisecond)
	out, _ := exec.Command("tmux", "-S", socket, "list-panes", "-t", "t", "-F", "#{pane_id}").Output()
	pane := trimLastNewline(string(out))
	t.Cleanup(func() { exec.Command("tmux", "-S", socket, "kill-server").Run() })

	sleep := exec.Command("sleep", "60")
	sleep.Start()
	t.Cleanup(func() { sleep.Process.Kill() })
	started, _ := binding.ProcStartTime(sleep.Process.Pid)

	cfg := Config{
		SocketPath: filepath.Join(dir, "bus.sock"), SpoolDir: filepath.Join(dir, "spool"),
		StatePath: filepath.Join(dir, "state.json"), RetryText: "continue", Tick: 100 * time.Millisecond,
	}
	os.MkdirAll(cfg.SpoolDir, 0700)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Run(ctx, cfg)
	waitSocket(t, cfg.SocketPath)
	post := func(m map[string]any) {
		c, err := net.Dial("unix", cfg.SocketPath)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		b, _ := json.Marshal(m)
		c.Write(append(b, '\n'))
	}
	post(map[string]any{"id": "d0", "kind": "session_start", "session_id": "s-D", "ts": 1,
		"payload": map[string]any{"tmux_socket": socket, "pane_id": pane, "pid": sleep.Process.Pid, "started_at": started}})
	reset := time.Now().Add(1 * time.Second).Format("2006-01-02 15:04:05")
	// 同一事故：socket 发一次 + spool 兜底一次（daemon 已在线，spool 由下次导入；此处直接 socket 双发同内容）
	post(map[string]any{"id": "d1", "kind": "stop_failure", "session_id": "s-D", "ts": 2,
		"payload": map[string]any{"error": "rate_limit", "last_assistant_message": "限额将在 " + reset + " 重置。已达到 5 小时的使用上限。"}})
	post(map[string]any{"id": "d2", "kind": "stop_failure", "session_id": "s-D", "ts": 3,
		"payload": map[string]any{"error": "rate_limit", "last_assistant_message": "限额将在 " + reset + " 重置。已达到 5 小时的使用上限。"}})

	time.Sleep(5 * time.Second)
	got := readFileOrEmpty(t, logF)
	if got != "continue\n" {
		t.Fatalf("duplicate reports must yield exactly one send, got %q", got)
	}
}

// 错误隔离：server_error 带限额样文案 → 不产生任何 incident
func TestMatrix_ErrorIsolation(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		SocketPath: filepath.Join(dir, "bus.sock"), SpoolDir: filepath.Join(dir, "spool"),
		StatePath: filepath.Join(dir, "state.json"), RetryText: "continue", Tick: 100 * time.Millisecond,
	}
	os.MkdirAll(cfg.SpoolDir, 0700)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Run(ctx, cfg)
	waitSocket(t, cfg.SocketPath)
	reset := time.Now().Add(time.Hour).Format("2006-01-02 15:04:05")
	c, _ := net.Dial("unix", cfg.SocketPath)
	b, _ := json.Marshal(map[string]any{"id": "x1", "kind": "stop_failure", "session_id": "s-X", "ts": 1,
		"payload": map[string]any{"error": "server_error",
			"last_assistant_message": "Request rejected (429) 限额将在 " + reset + " 重置。"}})
	c.Write(append(b, '\n'))
	c.Close()
	time.Sleep(1 * time.Second)
	// 解析 state：不应有任何 incident
	if _, err := os.Stat(cfg.StatePath); err == nil {
		data, _ := os.ReadFile(cfg.StatePath)
		var st struct {
			Incidents []map[string]any `json:"incidents"`
		}
		json.Unmarshal(data, &st)
		if len(st.Incidents) != 0 {
			t.Fatalf("server_error must not create incidents, got %v", st.Incidents)
		}
	}
}

func readFileOrEmpty(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(b)
}

func waitSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.Dial("unix", path); err == nil {
			c.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("socket %s never ready", path)
}

func trimLastNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
