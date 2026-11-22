// Package transcript 提供对 jsonl 追加式会话记录的 cursor 读取：
// 只交付完整行，半行悬在 pending；以 (dev, inode, offset) 为检查点，
// 文件被替换或截断时自动重扫。
package transcript

import (
	"bytes"
	"os"
	"syscall"
)

// tailScanWindow 是 NewCursor/Restore 时向后找最后一个换行的窗口。
const tailScanWindow = 1 << 20

type Cursor struct {
	path string
	dev  uint64
	ino  uint64
	off  int64 // 指向 pending 半行的起点（即已交付内容的末尾）
}

func statOf(path string) (dev, ino uint64, size int64, err error) {
	st, err := os.Stat(path)
	if err != nil {
		return 0, 0, 0, err
	}
	s := st.Sys().(*syscall.Stat_t)
	return uint64(s.Dev), s.Ino, st.Size(), nil
}

// initPending 把 off 对齐到最后一个换行之后，使尾部半行进入待补齐状态。
func (c *Cursor) initPending(size int64) error {
	window := int64(tailScanWindow)
	if window > size {
		window = size
	}
	buf := make([]byte, window)
	f, err := os.Open(c.path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.ReadAt(buf, size-window); err != nil && err != os.ErrClosed {
		_ = err // ReadAt 语义：EOF 短读也返回已读字节
	}
	if n := bytes.LastIndexByte(buf, '\n'); n >= 0 {
		c.off = size - window + int64(n) + 1
	} else {
		c.off = 0 // 窗口内无换行：整个窗口都是半行
	}
	return nil
}

// NewCursor 打开文件并定位到末尾完整行边界（已有的完整历史不重放）。
func NewCursor(path string) (*Cursor, error) {
	dev, ino, size, err := statOf(path)
	if err != nil {
		return nil, err
	}
	c := &Cursor{path: path, dev: dev, ino: ino}
	if err := c.initPending(size); err != nil {
		return nil, err
	}
	return c, nil
}

// RestoreCursor 从检查点恢复；inode 不符视为文件被替换，从头重扫。
func RestoreCursor(dev, ino uint64, off int64, path string) (*Cursor, error) {
	curDev, curIno, size, err := statOf(path)
	if err != nil {
		return nil, err
	}
	c := &Cursor{path: path, dev: curDev, ino: curIno}
	switch {
	case curDev != dev || curIno != ino:
		c.off = 0
	case off > size:
		c.off = 0 // 截断
	default:
		c.off = off
		if err := c.initPending(size); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// Checkpoint 返回持久化检查点（不含 pending 半行）。
func (c *Cursor) Checkpoint() (dev, ino uint64, off int64) {
	return c.dev, c.ino, c.off
}

// Poll 返回自上次调用以来新增的完整行；半行留待下次。
func (c *Cursor) Poll() ([][]byte, error) {
	dev, ino, size, err := statOf(c.path)
	if err != nil {
		return nil, err
	}
	if dev != c.dev || ino != c.ino {
		c.dev, c.ino, c.off = dev, ino, 0
	}
	if size < c.off {
		c.off = 0
	}
	if size == c.off {
		return nil, nil
	}
	buf := make([]byte, size-c.off)
	f, err := os.Open(c.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.ReadAt(buf, c.off); err != nil {
		return nil, err
	}
	parts := bytes.Split(buf, []byte("\n"))
	complete := parts[:len(parts)-1]
	pending := parts[len(parts)-1]
	c.off = size - int64(len(pending))
	if len(complete) == 0 {
		return nil, nil
	}
	return complete, nil
}
