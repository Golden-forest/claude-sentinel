package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// FSEvents 补偿通道：jsonl 追加限额报错行 → 无钩子也产生 incident
func TestDaemon_FsWatchChannel(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "projects", "-Users-hl-Projects-X")
	os.MkdirAll(root, 0700)
	tr := filepath.Join(root, "sess1.jsonl")
	os.WriteFile(tr, []byte(`{"type":"user"}`+"\n"), 0600)

	cfg := Config{
		SocketPath: filepath.Join(dir, "bus.sock"), SpoolDir: filepath.Join(dir, "spool"),
		StatePath: filepath.Join(dir, "state.json"), RetryText: "continue",
		Tick: 100 * time.Millisecond, WatchRoot: filepath.Join(dir, "projects"),
	}
	os.MkdirAll(cfg.SpoolDir, 0700)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Run(ctx, cfg)
	waitSocket(t, cfg.SocketPath)
	time.Sleep(300 * time.Millisecond) // fsevents 建立

	reset := time.Now().Add(time.Hour).Format("2006-01-02 15:04:05")
	f, _ := os.OpenFile(tr, os.O_APPEND|os.O_WRONLY, 0600)
	f.WriteString(`{"type":"assistant","isApiErrorMessage":true,"message":{"content":[{"type":"text","text":"API Error: Request rejected (429) · 已达到 5 小时的使用上限。您的限额将在 ` + reset + ` 重置。"}]}}` + "\n")
	f.Close()

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(cfg.StatePath)
		if err == nil {
			var st struct {
				Incidents []struct {
					ID    string `json:"id"`
					State string `json:"state"`
				} `json:"incidents"`
			}
			if json.Unmarshal(b, &st) == nil && len(st.Incidents) > 0 {
				if st.Incidents[0].State == "SCHEDULED" || st.Incidents[0].State == "NEEDS_REVIEW" {
					return // 通道打通：无钩子也有事故记录
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("fs channel produced no incident within 8s")
}
