// Package classify 识别限额类事故并解析可信的重置时间。
// 优先级：quotaLimits.resetsAt(epoch) > 中文重置正则；解析不出或过合理性窗口即拒绝。
package classify

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

type Incident struct {
	SessionID      string
	TranscriptPath string
	ResetAt        int64  // unix epoch 秒
	Source         string // "hook" | "jsonl"
	Pattern        string
}

// DefaultPatterns 与 ~/.claude-auto-retry.json 的 9 条一致（v1 遗产，配置可覆盖）。
var DefaultPatterns = []string{
	`已达到 5 小时的使用上限`,
	`限额将在.{0,40}重置`,
	`Request rejected \(429\)`,
	`Too Many Requests`,
	`rate.?limit`,
	`usage.?limit`,
	`\b429\b`,
	`余额不足`,
	`额度.{0,6}(用完|耗尽|不足)`,
}

var compiled = compileAll(DefaultPatterns)

func compileAll(pats []string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, len(pats))
	for i, p := range pats {
		out[i] = regexp.MustCompile(p)
	}
	return out
}

// quotaTypes：结构化 error 类型白名单（P0-1 真实限额事件落地后回填确切值）。
var quotaTypes = map[string]bool{
	"rate_limit": true,
}

var resetRe = regexp.MustCompile(`限额将在\s*(\d{4}-\d{2}-\d{2}\s+\d{2}:\d{2}:\d{2})\s*重置`)

const resetLayout = "2006-01-02 15:04:05"

type rawPayload struct {
	QuotaLimits *struct {
		Status        string `json:"status"`
		RateLimitType string `json:"rateLimitType"`
		ResetsAt      int64  `json:"resetsAt"`
	} `json:"quotaLimits"`
	LastAssistantMessage string `json:"last_assistant_message"`
	IsAPIErrorMessage    bool   `json:"isApiErrorMessage"`
	Message              *struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
}

// textOf 提取载荷中所有人类可读报错文本（hook 的 last_assistant_message 或 jsonl 的 message.content）。
func textOf(r rawPayload) string {
	if r.LastAssistantMessage != "" {
		return r.LastAssistantMessage
	}
	if r.Message != nil {
		var b strings.Builder
		for _, c := range r.Message.Content {
			b.WriteString(c.Text)
			b.WriteString("\n")
		}
		return b.String()
	}
	return ""
}

// ParseResetAt 按优先级解析重置时间；拿不到可信值返回 false。
func ParseResetAt(payload []byte) (int64, bool) {
	var r rawPayload
	_ = json.Unmarshal(payload, &r)
	if r.QuotaLimits != nil && r.QuotaLimits.ResetsAt > 0 {
		return r.QuotaLimits.ResetsAt, true
	}
	m := resetRe.FindStringSubmatch(textOf(r))
	if m == nil {
		return 0, false
	}
	ts, err := time.ParseInLocation(resetLayout, m[1], time.Local)
	if err != nil {
		return 0, false
	}
	return ts.Unix(), true
}

// ClassifyErrorAt 在给定时刻分类；errType 为空表示 watch 通道（只走文本正则）。
// errType 非空且不在白名单 → 拒绝（auth/billing/server 错误绝不触发续跑）。
func ClassifyErrorAt(errType string, payload []byte, now time.Time) (*Incident, bool) {
	if errType != "" && !quotaTypes[errType] {
		return nil, false
	}
	at, ok := ParseResetAt(payload)
	if !ok {
		return nil, false
	}
	if at < now.Add(-5*time.Minute).Unix() || at > now.Add(7*24*time.Hour).Unix() {
		return nil, false
	}
	var r rawPayload
	_ = json.Unmarshal(payload, &r)
	src := "jsonl"
	pattern := ""
	if r.LastAssistantMessage != "" {
		src = "hook"
	}
	if r.QuotaLimits != nil && r.QuotaLimits.ResetsAt > 0 {
		pattern = "quotaLimits.resetsAt"
	} else {
		for i, re := range compiled {
			if re.Match(payload) {
				pattern = DefaultPatterns[i]
				break
			}
		}
	}
	if pattern == "" {
		return nil, false
	}
	return &Incident{ResetAt: at, Source: src, Pattern: pattern}, true
}

// ClassifyError 是 ClassifyErrorAt 的当前时刻包装。
func ClassifyError(errType string, payload []byte) (*Incident, bool) {
	return ClassifyErrorAt(errType, payload, time.Now())
}
