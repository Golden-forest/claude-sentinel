// Package guard：发送 continue 前的七道闸门核验链。
// 任一不过即不发送；无法确定 → NEEDS_REVIEW，绝不猜测。
package guard

import (
	"github.com/Golden-forest/claude-sentinel/internal/binding"
	"github.com/Golden-forest/claude-sentinel/internal/machine"
)

type Verdict string

const (
	VerdictProceed     Verdict = "PROCEED"
	VerdictResolved    Verdict = "RESOLVED"
	VerdictCancelled   Verdict = "CANCELLED"
	VerdictNeedsReview Verdict = "NEEDS_REVIEW"
)

// Evidence 由 daemon 侧采集的核验事实（与采集方式解耦，便于测试）。
type Evidence struct {
	IncidentOpen            bool // incident 非终态
	BindingAlive            bool // 绑定的 claude 进程仍存活且启动时间一致
	PaneExists              bool // tmux pane 仍在
	PaneInCopyMode          bool // pane 处于 copy-mode
	ResetAtPassed           bool // epoch 重算后重置时刻已过
	NativeResumed           bool // 原生续跑已介入的信号
	NewerTranscriptActivity bool // 报错行之后 transcript 已有新进展
}

// Check 按核验链给出裁决。b 为 nil（无绑定）时绝不 PROCEED。
func Check(inc *machine.Incident, b *binding.Binding, ev Evidence) Verdict {
	if inc == nil {
		return VerdictNeedsReview
	}
	switch inc.State {
	case machine.StateCancelled:
		return VerdictCancelled
	case machine.StateResolved:
		return VerdictResolved
	}
	if ev.NativeResumed || ev.NewerTranscriptActivity {
		return VerdictResolved
	}
	if b == nil {
		return VerdictNeedsReview
	}
	if !ev.IncidentOpen || !ev.BindingAlive || !ev.PaneExists || ev.PaneInCopyMode || !ev.ResetAtPassed {
		return VerdictNeedsReview
	}
	return VerdictProceed
}
