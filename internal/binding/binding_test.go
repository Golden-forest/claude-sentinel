package binding

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestTable_PutGetInvalidate(t *testing.T) {
	tbl := NewTable()
	tbl.Put(Binding{SessionID: "s1", TmuxSocket: "/tmp/x", PaneID: "%1", Pid: 42, StartedAt: 100})
	b, ok := tbl.Get("s1")
	if !ok || b.PaneID != "%1" {
		t.Fatalf("get: %+v ok=%v", b, ok)
	}
	tbl.Invalidate("s1")
	if _, ok := tbl.Get("s1"); ok {
		t.Fatal("invalidate must remove binding")
	}
}

// 真实进程：活着 → 有效；被杀 → 失效
func TestStillValid_AliveThenDead(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	st, err := ProcStartTime(pid)
	if err != nil {
		t.Fatal(err)
	}
	b := Binding{SessionID: "s", Pid: pid, StartedAt: st}
	if !b.StillValid() {
		t.Fatal("live process with matching start time must be valid")
	}
	cmd.Process.Signal(syscall.SIGKILL)
	cmd.Wait()
	time.Sleep(100 * time.Millisecond)
	if b.StillValid() {
		t.Fatal("dead process must be invalid")
	}
}

// PID 复用防护：pid 活着但启动时间对不上 → 无效
func TestStillValid_PidReuseMismatch(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill() }()
	st, _ := ProcStartTime(cmd.Process.Pid)
	b := Binding{SessionID: "s", Pid: cmd.Process.Pid, StartedAt: st - 7}
	if b.StillValid() {
		t.Fatal("same pid but wrong start time must be invalid (reuse)")
	}
}
