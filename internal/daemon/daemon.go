// Package daemon：sentinel 常驻核心。事件入口 → 分类 → 状态机 → 到期核验 → 受控执行。
package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Golden-forest/claude-sentinel/internal/binding"
	"github.com/Golden-forest/claude-sentinel/internal/classify"
	"github.com/Golden-forest/claude-sentinel/internal/eventbus"
	"github.com/Golden-forest/claude-sentinel/internal/executor"
	"github.com/Golden-forest/claude-sentinel/internal/fswatch"
	"github.com/Golden-forest/claude-sentinel/internal/guard"
	"github.com/Golden-forest/claude-sentinel/internal/machine"
	"github.com/Golden-forest/claude-sentinel/internal/transcript"
)

type Config struct {
	SocketPath string
	SpoolDir   string
	StatePath  string
	TmuxSocket string // 兜底用；实际以绑定表里的会话所属 socket 为准
	RetryText  string
	Tick       time.Duration
	WatchRoot  string // transcript 根目录；空则启用 FSEvents 补偿通道
}

func Run(ctx context.Context, cfg Config) error {
	store, err := machine.OpenStore(cfg.StatePath)
	if err != nil {
		return err
	}
	bindings := binding.NewTable()
	events := make(chan eventbus.Event, 64)

	go eventbus.Serve(ctx, cfg.SocketPath, events)
	if n, err := eventbus.SpoolImport(cfg.SpoolDir, events); err == nil && n > 0 {
		log.Printf("[sentinel] spool imported %d events", n)
	}

	tick := cfg.Tick
	if tick <= 0 {
		tick = 5 * time.Second
	}
	fsChanges := make(chan string, 64)
	if cfg.WatchRoot != "" {
		baselineCursors(cfg.WatchRoot) // 先建基线，事件到达时 cursor 已就位
		if err := fswatch.Watch(ctx, cfg.WatchRoot, fsChanges); err != nil {
			log.Printf("[sentinel] fswatch unavailable: %v", err)
		}
	}

	ticker := time.NewTicker(tick)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev := <-events:
			handleEvent(store, bindings, ev)
		case p := <-fsChanges:
			handleFsChange(store, p)
		case <-ticker.C:
			sweep(store, bindings, cfg)
		}
	}
}

// cursors 由基线预建；并发仅事件循环单线程访问，无锁。
var cursors = map[string]*transcript.Cursor{}

// baselineCursors 把 WatchRoot 下全部 jsonl 的 cursor 预建到当前末尾，
// 避免「事件晚于写入到达 → 惰性建 cursor 踩掉触发行」。
func baselineCursors(root string) {
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".jsonl") {
			return nil
		}
		if real, err := filepath.EvalSymlinks(p); err == nil {
			p = real
		}
		if c, err := transcript.NewCursor(p); err == nil {
			cursors[p] = c
		}
		return nil
	})
}

func handleFsChange(store *machine.Store, path string) {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real // FSEvents 上报已解析路径，与基线键统一
	}
	c, ok := cursors[path]
	if !ok {
		nc, err := transcript.NewCursor(path)
		if err != nil {
			return
		}
		cursors[path] = nc
		c = nc
	}
	lines, err := c.Poll()
	if err != nil {
		delete(cursors, path)
		return
	}
	for _, ln := range lines {
		inc, ok := classify.ClassifyError("", ln) // watch 通道：类型未知，走文本正则
		if !ok {
			continue
		}
		var sid struct {
			SessionID string `json:"session_id"`
		}
		_ = json.Unmarshal(ln, &sid)
		m := machine.Incident{
			ID:        fmt.Sprintf("%s@%d", sid.SessionID, inc.ResetAt),
			SessionID: sid.SessionID, TranscriptPath: path,
			ResetAt: inc.ResetAt, Source: "jsonl", Pattern: inc.Pattern,
			State: machine.StateScheduled,
		}
		if m.SessionID == "" {
			m.SessionID = "unknown:" + path
			m.ID = m.SessionID + fmt.Sprintf("@%d", inc.ResetAt)
		}
		if err := store.Update(&m); err == nil {
			log.Printf("[sentinel] fs incident %s (via %s)", m.ID, m.Pattern)
		}
	}
}

type sessionPayload struct {
	TmuxSocket string `json:"tmux_socket"`
	PaneID     string `json:"pane_id"`
	Pid        int    `json:"pid"`
	StartedAt  int64  `json:"started_at"`
}

type failurePayload struct {
	Error                string `json:"error"`
	LastAssistantMessage string `json:"last_assistant_message"`
	TranscriptPath       string `json:"transcript_path"`
}

func handleEvent(store *machine.Store, bindings *binding.Table, ev eventbus.Event) {
	switch ev.Kind {
	case "session_start":
		var p sessionPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil || p.Pid == 0 || p.PaneID == "" {
			log.Printf("[sentinel] session_start unusable: %v", err)
			return
		}
		p.TmuxSocket = firstNonEmpty(p.TmuxSocket, defaultSocket())
		bindings.Put(binding.Binding{
			SessionID:  ev.SessionID,
			TmuxSocket: p.TmuxSocket,
			PaneID:     p.PaneID,
			Pid:        p.Pid,
			StartedAt:  p.StartedAt,
		})
		log.Printf("[sentinel] bound %s → pane %s (pid %d)", ev.SessionID, p.PaneID, p.Pid)
	case "session_end":
		bindings.Invalidate(ev.SessionID)
		log.Printf("[sentinel] unbound %s", ev.SessionID)
	case "stop_failure":
		var p failurePayload
		_ = json.Unmarshal(ev.Payload, &p)
		c, ok := classify.ClassifyError(p.Error, ev.Payload)
		if !ok {
			return // 非限额类或解析不出可信重置时间：不动作
		}
		m := machine.Incident{
			ID:             fmt.Sprintf("%s@%d", ev.SessionID, c.ResetAt),
			SessionID:      ev.SessionID,
			TranscriptPath: firstNonEmpty(ev.TranscriptPath, p.TranscriptPath),
			ResetAt:        c.ResetAt,
			Source:         c.Source,
			Pattern:        c.Pattern,
			State:          machine.StateScheduled,
		}
		if err := store.Update(&m); err != nil {
			log.Printf("[sentinel] store update: %v", err)
			return
		}
		log.Printf("[sentinel] incident %s scheduled (reset %d, via %s/%s)",
			m.ID, m.ResetAt, m.Source, m.Pattern)
	}
}

func sweep(store *machine.Store, bindings *binding.Table, cfg Config) {
	now := time.Now().Unix()
	for _, inc := range store.Active() {
		switch inc.State {
		case machine.StateSentUnconfirmed:
			// v0.1 单发即认：重复限额会以新 incident 再来（更大 ResetAt）
			inc.State = machine.StateResolved
			_ = store.Update(inc)
			continue
		case machine.StateScheduled, machine.StateDue:
		default:
			continue
		}
		if !machine.Due(inc, now) {
			continue
		}
		b, hasBinding := bindings.Get(inc.SessionID)
		var bPtr *binding.Binding
		ev := guard.Evidence{IncidentOpen: true, ResetAtPassed: true}
		if hasBinding {
			bPtr = &b
			ev.BindingAlive = b.StillValid()
			if info, err := executor.GetPaneInfo(b.TmuxSocket, b.PaneID); err == nil {
				ev.PaneExists = true
				ev.PaneInCopyMode = info.InCopyMode
			}
		}
		switch guard.Check(inc, bPtr, ev) {
		case guard.VerdictProceed:
			if err := executor.SendContinue(b.TmuxSocket, b.PaneID, firstNonEmpty(cfg.RetryText, "continue")); err != nil {
				log.Printf("[sentinel] send failed for %s: %v", inc.ID, err)
				continue
			}
			inc.State = machine.StateSentUnconfirmed
			inc.Attempts++
			_ = store.Update(inc)
			log.Printf("[sentinel] SENT continue → %s (pane %s)", inc.ID, b.PaneID)
		case guard.VerdictResolved:
			inc.State = machine.StateResolved
			_ = store.Update(inc)
		case guard.VerdictCancelled:
			inc.State = machine.StateCancelled
			_ = store.Update(inc)
		case guard.VerdictNeedsReview:
			inc.State = machine.StateNeedsReview
			_ = store.Update(inc)
			log.Printf("[sentinel] NEEDS_REVIEW %s (binding=%v alive=%v pane=%v copy=%v)",
				inc.ID, hasBinding, ev.BindingAlive, ev.PaneExists, ev.PaneInCopyMode)
		}
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func defaultSocket() string {
	return "/private/tmp/tmux-501/default"
}
