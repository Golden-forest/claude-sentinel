package daemon

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Golden-forest/claude-sentinel/internal/machine"
)

// C2：TMUX 环境变量带 ",pid,session" 后缀，必须拆出纯 socket 路径
func TestC2_ParseTmuxSocket(t *testing.T) {
	cases := map[string]string{
		"/private/tmp/tmux-501/default,1295,4": "/private/tmp/tmux-501/default",
		"/tmp/tmux-1000/main,99,0":             "/tmp/tmux-1000/main",
		"":                                     "",
	}
	for in, want := range cases {
		if got := ParseTmuxSocket(in); got != want {
			t.Fatalf("ParseTmuxSocket(%q) = %q, want %q", in, got, want)
		}
	}
}

// C3：spool 积压 70 条（> 通道缓冲 64）不得死锁 daemon 启动
func TestC3_SpoolBacklogNoDeadlock(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		SocketPath: filepath.Join(dir, "bus.sock"), SpoolDir: filepath.Join(dir, "spool"),
		StatePath: filepath.Join(dir, "state.json"), RetryText: "continue", Tick: 100 * time.Millisecond,
	}
	os.MkdirAll(cfg.SpoolDir, 0700)
	for i := 0; i < 70; i++ {
		p := map[string]any{"id": "bk" + string(rune('A'+i%26)) + string(rune('a'+i%26)) + time.Now().Format("150405.000000000"),
			"kind": "notification", "session_id": "s-bk", "ts": int64(i)}
		b, _ := json.Marshal(p)
		os.WriteFile(filepath.Join(cfg.SpoolDir, p["id"].(string)+".json"), b, 0600)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg) }()
	// 5 秒内：socket 可连（事件循环活着）且 spool 被清空
	waitSocket(t, cfg.SocketPath)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		es, _ := os.ReadDir(cfg.SpoolDir)
		if len(es) == 0 {
			return // 导入全部消化，无死锁
		}
		time.Sleep(100 * time.Millisecond)
	}
	es, _ := os.ReadDir(cfg.SpoolDir)
	t.Fatalf("spool backlog deadlocked: %d entries remain", len(es))
}

// I2：终态事故不得被同 ID 事件重新打开
func TestI2_TerminalNotReopened(t *testing.T) {
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
	post := func(id string) {
		c, _ := net.Dial("unix", cfg.SocketPath)
		defer c.Close()
		b, _ := json.Marshal(map[string]any{"id": id, "kind": "stop_failure", "session_id": "s-I2", "ts": 1,
			"payload": map[string]any{"error": "rate_limit",
				"last_assistant_message": "已达到 5 小时的使用上限。您的限额将在 " + reset + " 重置。"}})
		c.Write(append(b, '\n'))
	}
	post("i2a")
	time.Sleep(500 * time.Millisecond)
	// 手动置 RESOLVED（模拟发送后解决）
	store, err := machine.OpenStore(cfg.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	incs := store.Active()
	if len(incs) != 1 {
		t.Fatalf("setup: want 1 incident, got %d", len(incs))
	}
	incs[0].State = machine.StateResolved
	store.Update(incs[0])
	// 同 ID 事件再来
	post("i2b")
	time.Sleep(500 * time.Millisecond)
	store2, _ := machine.OpenStore(cfg.StatePath)
	after, ok := store2.Get(incs[0].ID)
	if !ok || after.State != machine.StateResolved {
		t.Fatalf("terminal incident must stay RESOLVED, got %+v ok=%v", after, ok)
	}
}

// I6：第二个实例必须快速退出
func TestI6_SingleInstance(t *testing.T) {
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

	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("second instance must error, got nil")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("second instance did not exit within 3s")
	}
}
