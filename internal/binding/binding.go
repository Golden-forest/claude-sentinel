// Package binding 维护 session_id ↔ tmux pane ↔ claude 进程 的确定性绑定。
// 绑定校验含进程启动时间，防 PID 复用；pane 级核验在 guard 包。
package binding

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Binding struct {
	SessionID   string
	TmuxSocket  string
	PaneID      string
	Pid         int
	StartedAt   int64 // 进程启动时刻（epoch 秒）
	CLAUDEMajor int
}

type Table struct {
	mu        sync.RWMutex
	bySession map[string]Binding
}

func NewTable() *Table {
	return &Table{bySession: make(map[string]Binding)}
}

func (t *Table) Put(b Binding) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.bySession[b.SessionID] = b
}

func (t *Table) Get(sessionID string) (Binding, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	b, ok := t.bySession[sessionID]
	return b, ok
}

func (t *Table) Invalidate(sessionID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.bySession, sessionID)
}

// ProcStartTime 取进程启动时刻（epoch 秒）。强制 LC_ALL=C 防 locale 差异。
func ProcStartTime(pid int) (int64, error) {
	cmd := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid))
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return 0, strconv.ErrSyntax
	}
	// C locale 下形如 "Sat Nov 22 20:26:34 2026"
	ts, err := time.ParseInLocation("Mon Jan _2 15:04:05 2006", s, time.Local)
	if err != nil {
		return 0, err
	}
	return ts.Unix(), nil
}

// StillValid：pid 活着且启动时刻与记录一致（PID 复用即失配）。
func (b *Binding) StillValid() bool {
	st, err := ProcStartTime(b.Pid)
	if err != nil {
		return false
	}
	return st == b.StartedAt
}
