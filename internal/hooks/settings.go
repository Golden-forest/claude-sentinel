// Package hooks：Claude Code settings.json 的结构化合并——只增删带自有标记的条目，
// 绝不动他人 hooks 与无关设置。
package hooks

import (
	"encoding/json"
	"os"
)

type Entry struct {
	Event   string // StopFailure | SessionStart | SessionEnd | Notification
	Matcher string
	Command string
}

type hookEntry struct {
	Matcher any   `json:"matcher"`
	Hooks   []cmd `json:"hooks"`
}

type cmd struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

func load(p string) (map[string]any, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	var d map[string]any
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, err
	}
	if d == nil {
		d = map[string]any{}
	}
	return d, nil
}

func save(p string, d map[string]any) error {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0600)
}

// MergeHook 按 Command 前缀判重，幂等合入。
func MergeHook(p string, e Entry) error {
	d, err := load(p)
	if err != nil {
		return err
	}
	hooksAny, _ := d["hooks"].(map[string]any)
	if hooksAny == nil {
		hooksAny = map[string]any{}
		d["hooks"] = hooksAny
	}
	listAny, _ := hooksAny[e.Event].([]any)
	var kept []any
	for _, x := range listAny {
		if m, ok := x.(map[string]any); ok {
			if subs, ok := m["hooks"].([]any); ok {
				for _, s := range subs {
					if sc, ok := s.(map[string]any); ok {
						if c, _ := sc["command"].(string); c == e.Command {
							// 已存在：跳过（幂等）
							return save(p, d)
						}
					}
				}
			}
		}
		kept = append(kept, x)
	}
	hooksAny[e.Event] = append(kept, map[string]any{
		"matcher": e.Matcher,
		"hooks": []cmd{{
			Type: "command", Command: e.Command, Timeout: 5,
		}},
	})
	return save(p, d)
}

// RemoveOwnHooks 删除 command 含 ownPrefix 的条目；事件组空了整组移除。
func RemoveOwnHooks(p string, ownPrefix string) error {
	d, err := load(p)
	if err != nil {
		return err
	}
	hooksAny, _ := d["hooks"].(map[string]any)
	for event, listAny := range hooksAny {
		list, ok := listAny.([]any)
		if !ok {
			continue
		}
		var kept []any
		for _, x := range list {
			m, ok := x.(map[string]any)
			if !ok {
				kept = append(kept, x)
				continue
			}
			subs, _ := m["hooks"].([]any)
			var keptSubs []any
			for _, s := range subs {
				if sc, ok := s.(map[string]any); ok {
					if c, _ := sc["command"].(string); len(c) >= len(ownPrefix) && c[:len(ownPrefix)] == ownPrefix {
						continue
					}
				}
				keptSubs = append(keptSubs, s)
			}
			if len(keptSubs) == 0 {
				continue // 该条目全是我们：整条删
			}
			m["hooks"] = keptSubs
			kept = append(kept, m)
		}
		if len(kept) == 0 {
			delete(hooksAny, event)
		} else {
			hooksAny[event] = kept
		}
	}
	return save(p, d)
}
