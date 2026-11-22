package eventbus

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSpool_RoundTripAndDedup(t *testing.T) {
	dir := t.TempDir()
	e1 := Event{ID: "ev-1", Kind: "stop_failure", SessionID: "s1", Payload: []byte(`{}`), Ts: 1}
	if err := SpoolWrite(dir, e1); err != nil {
		t.Fatal(err)
	}
	// 同 ID 重复投递
	if err := SpoolWrite(dir, e1); err != nil {
		t.Fatal(err)
	}
	// 不同 ID
	if err := SpoolWrite(dir, Event{ID: "ev-2", Kind: "session_start", Ts: 2}); err != nil {
		t.Fatal(err)
	}
	out := make(chan Event, 8)
	n, err := SpoolImport(dir, out)
	if err != nil {
		t.Fatal(err)
	}
	close(out)
	var got []Event
	for e := range out {
		got = append(got, e)
	}
	if n != 2 || len(got) != 2 {
		t.Fatalf("dedup failed: n=%d got=%d", n, len(got))
	}
	// 导入后 spool 清空
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("spool not drained: %d entries", len(entries))
	}
}

func TestSpool_RejectsSymlinkDir(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "spoollink")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	err := SpoolWrite(link, Event{ID: "x", Ts: 1})
	if err == nil {
		t.Fatal("symlinked spool dir must be rejected")
	}
}

func TestServe_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "bus.sock")
	out := make(chan Event, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Serve(ctx, sock, out)

	deadline := time.Now().Add(2 * time.Second)
	var conn net.Conn
	var err error
	for time.Now().Before(deadline) { // 等 socket 就绪
		conn, err = net.Dial("unix", sock)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	want := Event{ID: "ev-9", Kind: "stop_failure", SessionID: "s9", TranscriptPath: "/x.jsonl", Ts: 9}
	b, _ := json.Marshal(want)
	conn.Write(append(b, '\n'))

	select {
	case got := <-out:
		if got.ID != want.ID || got.SessionID != want.SessionID {
			t.Fatalf("round trip mismatch: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event not delivered")
	}
}
