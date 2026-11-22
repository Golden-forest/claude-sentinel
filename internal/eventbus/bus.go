// Package eventbus：事件入口。socket 服务端（常驻通道）+ spool 目录（daemon 离线兜底）。
package eventbus

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type Event struct {
	ID             string          `json:"id"`
	Kind           string          `json:"kind"` // stop_failure | session_start | session_end | notification | fs
	SessionID      string          `json:"session_id,omitempty"`
	TranscriptPath string          `json:"transcript_path,omitempty"`
	Payload        json.RawMessage `json:"payload,omitempty"`
	Ts             int64           `json:"ts"`
}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// SpoolWrite 把事件原子写入 spool 目录；同 ID 同名文件自然去重。
// 目录是符号链接 → 拒绝（防路径劫持）。
func SpoolWrite(dir string, e Event) error {
	st, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("spool dir %s is a symlink: rejected", dir)
	}
	name := unsafeChars.ReplaceAllString(e.ID, "_")
	if name == "" || name == "_" {
		name = fmt.Sprintf("ev-%d", e.Ts)
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".spool-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	return os.Rename(tmpName, filepath.Join(dir, name+".json"))
}

// SpoolImport 导入并清空 spool，返回导入条数。
func SpoolImport(dir string, out chan<- Event) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	names := make([]string, 0, len(entries))
	for _, en := range entries {
		if strings.HasPrefix(en.Name(), ".") || !strings.HasSuffix(en.Name(), ".json") {
			continue
		}
		names = append(names, en.Name())
	}
	sort.Strings(names)
	n := 0
	for _, nm := range names {
		b, err := os.ReadFile(filepath.Join(dir, nm))
		if err != nil {
			continue
		}
		var e Event
		if err := json.Unmarshal(b, &e); err != nil {
			continue // 坏文件跳过并清走
		}
		out <- e
		n++
		if err := os.Remove(filepath.Join(dir, nm)); err != nil {
			return n, err
		}
	}
	return n, nil
}

// Serve 在 unix socket 上收事件并投递到 out，直到 ctx 取消。
func Serve(ctx context.Context, sockPath string, out chan<- Event) error {
	_ = os.Remove(sockPath) // 陈旧 socket
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return err
	}
	defer ln.Close()
	os.Chmod(sockPath, 0600)
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				return err
			}
		}
		go func(c net.Conn) {
			defer c.Close()
			sc := bufio.NewScanner(c)
			sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
			for sc.Scan() {
				var e Event
				if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
					continue
				}
				select {
				case out <- e:
				case <-ctx.Done():
					return
				}
			}
		}(conn)
	}
}
