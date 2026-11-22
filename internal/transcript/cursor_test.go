package transcript

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path string, s string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(s), 0600); err != nil {
		t.Fatal(err)
	}
}

func appendFile(t *testing.T, path string, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

// 半行不得被当坏数据：写入半行 → Poll 空；补齐换行后 → 返回整行
func TestPoll_HalfLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeFile(t, path, `{"a":1}`)
	c, err := NewCursor(path)
	if err != nil {
		t.Fatal(err)
	}
	lines, err := c.Poll()
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 0 {
		t.Fatalf("half line must not surface, got %d lines", len(lines))
	}
	appendFile(t, path, "\n{\"b\":2}\n")
	lines, err = c.Poll()
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || string(lines[0]) != `{"a":1}` || string(lines[1]) != `{"b":2}` {
		t.Fatalf("lines = %q", lines)
	}
}

// checkpoint 恢复：同 inode 从 offset 续读
func TestRestore_SameInode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeFile(t, path, "{\"a\":1}\n{\"a\":2}\n")
	c, _ := NewCursor(path)
	if _, err := c.Poll(); err != nil {
		t.Fatal(err)
	}
	dev, ino, off := c.Checkpoint()
	appendFile(t, path, "{\"a\":3}\n")
	c2, err := RestoreCursor(dev, ino, off, path)
	if err != nil {
		t.Fatal(err)
	}
	lines, err := c2.Poll()
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || string(lines[0]) != `{"a":3}` {
		t.Fatalf("resume lines = %q", lines)
	}
}

// inode 变化（文件被原子替换：temp+rename）→ 重扫全文件
func TestRestore_InodeChanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeFile(t, path, "{\"old\":1}\n")
	c, _ := NewCursor(path)
	c.Poll()
	dev, ino, off := c.Checkpoint()
	tmp := path + ".tmp"
	writeFile(t, tmp, "{\"new\":1}\n")
	if err := os.Rename(tmp, path); err != nil { // 真替换 → 新 inode
		t.Fatal(err)
	}
	c2, err := RestoreCursor(dev, ino, off, path)
	if err != nil {
		t.Fatal(err)
	}
	lines, err := c2.Poll()
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || string(lines[0]) != `{"new":1}` {
		t.Fatalf("replaced file must rescan, got %q", lines)
	}
}

// 文件截断（比 offset 短）→ 重扫
func TestPoll_Truncated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeFile(t, path, "{\"long-line-1\":1}\n{\"long-line-2\":2}\n")
	c, _ := NewCursor(path)
	c.Poll()
	writeFile(t, path, "{\"s\":1}\n") // 变短
	lines, err := c.Poll()
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || string(lines[0]) != `{"s":1}` {
		t.Fatalf("truncated file must rescan, got %q", lines)
	}
}
