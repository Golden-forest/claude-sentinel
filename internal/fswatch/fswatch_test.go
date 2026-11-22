package fswatch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatch_AppendSurfaces(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "s.jsonl")
	os.WriteFile(path, []byte("{\"a\":1}\n"), 0600)

	out := make(chan string, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := Watch(ctx, root, out); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond) // watch 建立窗口

	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	f.WriteString("{\"b\":2}\n")
	f.Close()

	select {
	case p := <-out:
		if filepath.Base(p) != "s.jsonl" {
			t.Fatalf("unexpected path %q", p)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("append did not surface within 3s")
	}
}

func TestWatch_IgnoresNonJsonl(t *testing.T) {
	root := t.TempDir()
	out := make(chan string, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := Watch(ctx, root, out); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	os.WriteFile(filepath.Join(root, "notes.txt"), []byte("x"), 0600)
	select {
	case p := <-out:
		t.Fatalf("non-jsonl surfaced: %q", p)
	case <-time.After(800 * time.Millisecond):
		// 预期：静默
	}
}
