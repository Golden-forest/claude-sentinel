package executor

import (
	"os"
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
