// Package fswatch：macOS FSEvents 补偿监听源。只回答「哪些 jsonl 可能变了」；
// 处理进度由 transcript cursor 负责，不依赖事件完整性。
package fswatch

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/rjeczalik/notify"
)

// Watch 递归监听 root，把发生变化的 *.jsonl 路径投到 out。
func Watch(ctx context.Context, root string, out chan<- string) error {
	events := make(chan notify.EventInfo, 64)
	if err := notify.Watch(filepath.Join(root, "..."), events, notify.Create, notify.Write); err != nil {
		return err
	}
	go func() {
		defer notify.Stop(events)
		for {
			select {
			case <-ctx.Done():
				return
			case ei := <-events:
				p := ei.Path()
				if strings.HasSuffix(p, ".jsonl") {
					select {
					case out <- p:
					default: // 消费端阻塞时丢弃事件——cursor 会补齐
					}
				}
			}
		}
	}()
	return nil
}
