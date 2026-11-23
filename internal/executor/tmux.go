// Package executor：tmux 受控执行。一律参数数组 + exec.CommandContext 语义，
// 文本与 Enter 分离发送，禁止 shell 拼接。
package executor

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func tmuxCmd(socket string, args ...string) *exec.Cmd {
	full := append([]string{"-S", socket}, args...)
	return exec.Command("tmux", full...)
}

func tmuxRun(socket string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := tmuxCmd(socket, args...)
	out, err := exec.CommandContext(ctx, "tmux", c.Args[1:]...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("tmux %v: %v: %s", args, err, out)
	}
	return string(out), nil
}

// SendContinue 三步：C-u 清行（重试防叠字）→ -l 字面文本 → 单独 Enter。
func SendContinue(socket, paneID, text string) error {
	if _, err := tmuxRun(socket, "send-keys", "-t", paneID, "C-u"); err != nil {
		return err
	}
	if _, err := tmuxRun(socket, "send-keys", "-t", paneID, "-l", text); err != nil {
		return err
	}
	_, err := tmuxRun(socket, "send-keys", "-t", paneID, "Enter")
	return err
}

type PaneInfo struct {
	ID             string
	CurrentCommand string
	InCopyMode     bool
	ForegroundPid  int
}

// GetPaneInfo 用 display-message 单次查询 pane 身份与状态。
// 分隔符用逗号不用 \t：launchd 环境无 LANG，tmux 在 C locale 下会把 \t 消毒成 _（C9）。
func GetPaneInfo(socket, paneID string) (PaneInfo, error) {
	out, err := tmuxRun(socket, "display-message", "-t", paneID,
		"-p", "#{pane_id},#{pane_current_command},#{pane_in_copy_mode},#{pane_pid}")
	if err != nil {
		return PaneInfo{}, err
	}
	parts := strings.Split(strings.TrimSpace(out), ",")
	if len(parts) != 4 {
		return PaneInfo{}, fmt.Errorf("unexpected display-message output: %q", out)
	}
	pid, _ := strconv.Atoi(parts[3])
	return PaneInfo{
		ID:             parts[0],
		CurrentCommand: parts[1],
		InCopyMode:     parts[2] == "1",
		ForegroundPid:  pid,
	}, nil
}
