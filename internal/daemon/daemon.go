// Package daemon：sentinel 常驻核心。事件入口 → 分类 → 状态机 → 到期核验 → 受控执行。
package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/Golden-forest/claude-sentinel/internal/binding"
	"github.com/Golden-forest/claude-sentinel/internal/classify"
	"github.com/Golden-forest/claude-sentinel/internal/eventbus"
	"github.com/Golden-forest/claude-sentinel/internal/executor"
	"github.com/Golden-forest/claude-sentinel/internal/guard"
	"github.com/Golden-forest/claude-sentinel/internal/machine"
)

type Config struct {
	SocketPath string
	SpoolDir   string
	StatePath  string
	TmuxSocket string // 兜底用；实际以绑定表里的会话所属 socket 为准
	RetryText  string
	Tick       time.Duration
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
	ticker := time.NewTicker(tick)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev := <-events:
			handleEvent(store, bindings, ev)
		case <-ticker.C:
			sweep(store, bindings, cfg)
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
