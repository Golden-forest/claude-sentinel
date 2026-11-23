package executor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testSocket = "testsent-go"

func setupPane(t *testing.T, cmd string) string {
	t.Helper()
	cleanup(t)
	run := func(args ...string) string {
		c := tmuxCmd(testSocket, args...)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("tmux %v: %v: %s", args, err, out)
		}
		return string(out)
	}
	run("new-session", "-d", "-s", "t", cmd)
	time.Sleep(300 * time.Millisecond)
	return strings.TrimSpace(run("list-panes", "-t", "t", "-F", "#{pane_id}"))
}

func cleanup(t *testing.T) {
	c := tmuxCmd(testSocket, "kill-server")
	c.Run()
}

func TestSendContinue_TextEnterSeparated(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "keys.log")
	pane := setupPane(t, "cat > "+logFile)

	if err := SendContinue(testSocket, pane, "continue"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	defer cleanup(t)

	b, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	// pty 行规程 ICRNL 会把 Enter 键翻译为 \n；字面文本 + 恰一个换行 = 分离成立
	if got != "continue\n" {
		t.Fatalf("keys must be literal text then exactly one Enter, got %q", got)
	}
}

func TestGetPaneInfo(t *testing.T) {
	pane := setupPane(t, "sleep 60")
	defer cleanup(t)
	info, err := GetPaneInfo(testSocket, pane)
	if err != nil {
		t.Fatal(err)
	}
	if info.ID != pane {
		t.Fatalf("ID = %q, want %q", info.ID, pane)
	}
	if info.InCopyMode {
		t.Fatal("fresh pane must not be in copy mode")
	}
	if info.ForegroundPid <= 0 {
		t.Fatalf("ForegroundPid = %d", info.ForegroundPid)
	}
}

// C9（launchd 实况，2026-11-24 生产事故）：守护进程环境没有 LANG，tmux 在
// C locale 下会把格式串里的 \t 消毒成 _，\t 分隔当场解析失败——生产 9 次
// 事故全部 pane=false 的根因。Go 的 os.Unsetenv 不影响子进程，所以用
// 子进程重执行精确复现 launchd 的裸环境。
func TestGetPaneInfo_BareEnv(t *testing.T) {
	if os.Getenv("SENTINEL_BARE_CHILD") == "1" {
		info, err := GetPaneInfo(testSocket, os.Getenv("TEST_PANE"))
		if err != nil {
			fmt.Println("CHILD_ERR:", err)
			os.Exit(2)
		}
		fmt.Println("CHILD_OK:", info.ID, info.ForegroundPid)
		return
	}
	pane := setupPane(t, "sleep 60")
	defer cleanup(t)
	cmd := exec.Command(os.Args[0], "-test.run=TestGetPaneInfo_BareEnv", "-test.v=false")
	cmd.Env = []string{
		"SENTINEL_BARE_CHILD=1",
		"TEST_PANE=" + pane,
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bare-env child must parse pane info (launchd reality), child said: %s (err=%v)", out, err)
	}
	if !strings.Contains(string(out), "CHILD_OK: "+pane) {
		t.Fatalf("child reported wrong pane: %s", out)
	}
}
