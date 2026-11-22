// Package machine 提供 incident 状态机的崩溃安全持久化：
// flock 互斥、临时文件+fsync+原子 rename+目录 fsync、损坏隔离恢复。
package machine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type State string

const (
	StateDetected        State = "DETECTED"
	StateClassified      State = "CLASSIFIED"
	StateScheduled       State = "SCHEDULED"
	StateDue             State = "DUE"
	StateVerifying       State = "VERIFYING"
	StateSentUnconfirmed State = "SENT_UNCONFIRMED"
	StateResolved        State = "RESOLVED"
	StateCancelled       State = "CANCELLED"
	StateNeedsReview     State = "NEEDS_REVIEW"
)

func terminal(s State) bool {
	return s == StateResolved || s == StateCancelled || s == StateNeedsReview
}

type Incident struct {
	ID             string `json:"id"`
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	ResetAt        int64  `json:"reset_at"`
	Source         string `json:"source"`
	Pattern        string `json:"pattern"`
	State          State  `json:"state"`
	Attempts       int    `json:"attempts"`
	UpdatedAt      int64  `json:"updated_at"`
}

// Due 只看绝对 epoch，任何墙钟漂移都在调用侧重算后传入。
func Due(inc *Incident, now int64) bool { return now >= inc.ResetAt }

type persisted struct {
	Version   int        `json:"version"`
	Seq       int64      `json:"seq"`
	Incidents []*Incident `json:"incidents"`
}

type Store struct {
	path string

	crashBeforeRename bool // 仅测试注入
}

func lockPath(p string) string { return p + ".lock" }

func withFlock(p string, fn func() error) error {
	f, err := os.OpenFile(lockPath(p), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}

func fsyncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	defer d.Close()
	_ = d.Sync()
}

func load(p string) (*persisted, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return &persisted{Version: 1}, nil
		}
		return nil, err
	}
	var d persisted
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, err
	}
	if d.Version == 0 {
		d.Version = 1
	}
	return &d, nil
}

// OpenStore 打开（或创建）状态文件。损坏 → 隔离改名并尝试从备份恢复，
// 返回提及 quarantine 的错误——绝不静默当空。
func OpenStore(path string) (*Store, error) {
	s := &Store{path: path}
	if _, err := load(path); err != nil {
		quar := fmt.Sprintf("%s.quarantine-%d", path, time.Now().Unix())
		if rerr := os.Rename(path, quar); rerr != nil {
			return nil, fmt.Errorf("state corrupt and quarantine failed: %v / %v", err, rerr)
		}
		// 从备份恢复
		if bak, berr := load(path + ".bak"); berr == nil {
			if werr := s.writeAll(bak); werr == nil {
				return nil, fmt.Errorf("state corrupt: quarantined to %s, restored from backup: %v", filepath.Base(quar), err)
			}
		}
		// 无可用备份：落一份空状态，让下次能开工，但本次必须报错
		_ = s.writeAll(&persisted{Version: 1})
		return nil, fmt.Errorf("state corrupt: quarantined to %s, no backup available: %v", filepath.Base(quar), err)
	}
	return s, nil
}

func (s *Store) writeAll(d *persisted) error {
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".state-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // rename 成功后是 no-op
	b, _ := json.MarshalIndent(d, "", " ")
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	if s.crashBeforeRename {
		os.Exit(70) // 测试注入：rename 前暴毙
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return err
	}
	os.Chmod(s.path, 0600)
	_ = os.WriteFile(s.path+".bak", b, 0600) // 始终保持一份 last-good 备份（首次写入也有）
	fsyncDir(dir)
	return nil
}

// Update 幂等 upsert（按 ID），flock + 原子写。
func (s *Store) Update(inc *Incident) error {
	inc.UpdatedAt = time.Now().Unix()
	return withFlock(s.path, func() error {
		d, err := load(s.path)
		if err != nil {
			return err
		}
		replaced := false
		for i, x := range d.Incidents {
			if x.ID == inc.ID {
				d.Incidents[i] = inc
				replaced = true
				break
			}
		}
		if !replaced {
			d.Incidents = append(d.Incidents, inc)
		}
		d.Seq++
		return s.writeAll(d)
	})
}

func (s *Store) Get(id string) (*Incident, bool) {
	d, err := load(s.path)
	if err != nil {
		return nil, false
	}
	for _, x := range d.Incidents {
		if x.ID == id {
			return x, true
		}
	}
	return nil, false
}

// Active 返回所有非终态 incident。
func (s *Store) Active() []*Incident {
	d, err := load(s.path)
	if err != nil {
		return nil
	}
	var out []*Incident
	for _, x := range d.Incidents {
		if !terminal(x.State) {
			out = append(out, x)
		}
	}
	return out
}
