package fswatch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatch_RecursiveNested(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "a", "b")
	os.MkdirAll(nested, 0700)
	path := filepath.Join(nested, "s.jsonl")
	os.WriteFile(path, []byte("{\"a\":1}\n"), 0600)
	out := make(chan string, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := Watch(ctx, root, out); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	f.WriteString("{\"b\":2}\n")
	f.Close()
	select {
	case p := <-out:
		t.Logf("got %s", p)
	case <-time.After(4 * time.Second):
		t.Fatal("nested append never surfaced — recursion broken")
	}
}
