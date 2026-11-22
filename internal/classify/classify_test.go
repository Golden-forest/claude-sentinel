package classify

import (
	"testing"
	"time"
)

// 真实样本（2026-11-20 13:57 智谱限额报错，从 VoiceBridge 会话 jsonl 提取）
const realGLMLine = `{"type":"assistant","isApiErrorMessage":true,"message":{"content":[{"type":"text","text":"API Error: Request rejected (429) · [1308][已达到 5 小时的使用上限。您的限额将在 2026-11-20 17:47:08 重置。][20261120135713effef347b24842da]"}]}}`

// 真实样本（2026-11-22 StopFailure 钩子实测载荷形状）
const realHookPayload = `{"session_id":"0ba3729d","transcript_path":"/x.jsonl","hook_event_name":"StopFailure","error":"rate_limit","last_assistant_message":"API Error: Request rejected (429) · [1308][已达到 5 小时的使用上限。您的限额将在 2026-11-20 17:47:08 重置。][x]"}`

func at(y int, mo time.Month, d, h, mi, s int) time.Time {
	return time.Date(y, mo, d, h, mi, s, 0, time.Local)
}

// 结构化第一优先级：quotaLimits.resetsAt epoch
func TestClassifyError_StructuredQuota(t *testing.T) {
	jline := []byte(`{"type":"assistant","isApiErrorMessage":true,"quotaLimits":{"status":"rejected","rateLimitType":"five_hour","resetsAt":1795185072}}`)
	now := time.Unix(1795185072-3600, 0)
	inc, ok := ClassifyErrorAt("rate_limit", jline, now)
	if !ok {
		t.Fatal("expected ok for structured quota line")
	}
	if inc.ResetAt != 1795185072 {
		t.Fatalf("ResetAt = %d, want 1795185072", inc.ResetAt)
	}
	if inc.Source != "jsonl" {
		t.Fatalf("Source = %q, want jsonl", inc.Source)
	}
}

// 钩子载荷：无 quotaLimits，但 last_assistant_message 带中文重置文本（P0-1 实测路径）
func TestClassifyError_HookMessage(t *testing.T) {
	now := at(2026, 11, 20, 13, 57, 18)
	want := at(2026, 11, 20, 17, 47, 8).Unix()
	inc, ok := ClassifyErrorAt("rate_limit", []byte(realHookPayload), now)
	if !ok {
		t.Fatal("expected ok for hook payload")
	}
	if inc.ResetAt != want {
		t.Fatalf("ResetAt = %d, want %d", inc.ResetAt, want)
	}
	if inc.Source != "hook" {
		t.Fatalf("Source = %q, want hook", inc.Source)
	}
}

// 降级正则：裸 jsonl 行（无结构化字段）命中 9 条默认模式之一
func TestClassifyError_RegexFallback(t *testing.T) {
	now := at(2026, 11, 20, 13, 57, 0)
	want := at(2026, 11, 20, 17, 47, 8).Unix()
	inc, ok := ClassifyErrorAt("", []byte(realGLMLine), now)
	if !ok {
		t.Fatal("expected ok via regex fallback")
	}
	if inc.ResetAt != want {
		t.Fatalf("ResetAt = %d, want %d", inc.ResetAt, want)
	}
	if inc.Pattern == "" {
		t.Fatal("Pattern should record which regex matched")
	}
}

// 歧义时间必须拒绝：无日期的 "3pm"
func TestParseResetAt_RejectsAmbiguous(t *testing.T) {
	if _, ok := ParseResetAt([]byte(`{"last_assistant_message":"limit resets at 3pm"}`)); ok {
		t.Fatal("ambiguous '3pm' must be rejected")
	}
}

// 合理性窗口：过期/过远都拒绝
func TestClassifyError_SanityWindow(t *testing.T) {
	old := []byte(`{"quotaLimits":{"resetsAt":1000}}`) // 远古
	if _, ok := ClassifyErrorAt("rate_limit", old, time.Now()); ok {
		t.Fatal("resetsAt in the deep past must be rejected")
	}
	far := []byte(`{"last_assistant_message":"已达到 5 小时的使用上限。您的限额将在 2026-12-31 17:47:08 重置。"}`)
	if _, ok := ClassifyErrorAt("rate_limit", far, at(2026, 11, 20, 13, 0, 0)); ok {
		t.Fatal("resetsAt beyond +7d must be rejected")
	}
}

// 非限额错误类型不得触发
func TestClassifyError_NonQuotaTypeRejected(t *testing.T) {
	jline := []byte(`{"last_assistant_message":"API Error: Request rejected (429) · 限额将在 2026-11-20 17:47:08 重置"}`)
	if _, ok := ClassifyErrorAt("server_error", jline, at(2026, 11, 20, 13, 0, 0)); ok {
		t.Fatal("server_error with quota-looking text must NOT classify (auth/billing/plain errors stay out)")
	}
}
