package machine

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newInc(id string, reset int64) *Incident {
	return &Incident{ID: id, SessionID: "s-" + id, ResetAt: reset, State: StateScheduled}
}

func TestStore_RoundTripAndActive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(time.Hour).Unix()
	if err := s.Update(newInc("i1", now)); err != nil {
		t.Fatal(err)
	}
	s2, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := s2.Get("i1")
	if !ok || got.ResetAt != now {
		t.Fatalf("round trip lost incident: %+v ok=%v", got, ok)
	}
	if len(s2.Active()) != 1 {
		t.Fatalf("Active = %d, want 1", len(s2.Active()))
	}
	// 终态后不再 Active
	got.State = StateResolved
	if err := s2.Update(got); err != nil {
		t.Fatal(err)
	}
	if len(s2.Active()) != 0 {
		t.Fatalf("resolved incident still active")
	}
}

// 崩溃一致性：子进程在 rename 前暴毙 → 旧 state.json 必须完整
func TestStore_CrashBeforeRename(t *testing.T) {
	if os.Getenv("SENTINEL_CRASH_CHILD") == "1" {
		path := os.Getenv("SENTINEL_STATE")
		s, _ := OpenStore(path)
		s.Update(newInc("old", time.Now().Add(time.Hour).Unix()))
		s.crashBeforeRename = true
		_ = s.Update(newInc("new", time.Now().Add(2*time.Hour).Unix()))
		os.Exit(0) // 到不了这里；crash hook 先杀
	}
	path := filepath.Join(t.TempDir(), "state.json")
	s, _ := OpenStore(path)
	s.Update(newInc("old", time.Now().Add(time.Hour).Unix()))

	cmd := exec.Command(os.Args[0], "-test.run", "TestStore_CrashBeforeRename")
	cmd.Env = append(os.Environ(), "SENTINEL_CRASH_CHILD=1", "SENTINEL_STATE="+path)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	_ = cmd.Run() // 子进程崩溃，退出码忽略

	s2, err := OpenStore(path)
	if err != nil {
		t.Fatalf("state corrupted by crash: %v", err)
	}
	if _, ok := s2.Get("new"); ok {
		t.Fatal("crashed write must not be visible")
	}
	if _, ok := s2.Get("old"); !ok {
		t.Fatal("pre-crash state must survive intact")
	}
}

// 损坏处置：隔离改名 + 从备份恢复；绝不静默当空
func TestStore_CorruptQuarantines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	s, _ := OpenStore(path)
	s.Update(newInc("keep", time.Now().Add(time.Hour).Unix()))

	os.WriteFile(path, []byte(`{"seq":3, "incidents":[ GARBAGE`), 0600) // 损坏当前文件

	_, err := OpenStore(path)
	if err == nil {
		t.Fatal("corrupt state must surface an error, not pass silently")
	}
	if !strings.Contains(err.Error(), "quarantine") {
		t.Fatalf("error should mention quarantine, got: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "state.json.quarantine-*"))
	if len(matches) == 0 {
		t.Fatal("corrupt file must be renamed to quarantine")
	}
	// 备份恢复后数据还在
	s3, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s3.Get("keep"); !ok {
		t.Fatal("backup restore lost the incident")
	}
}

// deadline 一律 epoch：判到期只比较绝对时间
func TestDue_EpochOnly(t *testing.T) {
	inc := newInc("d1", 1000)
	if Due(inc, 999) {
		t.Fatal("not yet due")
	}
	if !Due(inc, 1000) {
		t.Fatal("due at exact epoch")
	}
}
