# claude-sentinel 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 用 Go 实现事件驱动的 Claude Code 额度自愈工具，替代 claude-auto-retry（v1.1 spec）。

**Architecture:** 单调度器守护进程（Router/状态机/Guard/执行器）+ 三个无轮询事件入口（StopFailure/生命周期钩子走 Unix socket + spool 兜底、FSEvents 补偿）。发送前七道闸门核验，拿不准绝不操作。

**Tech Stack:** Go（标准库优先；外部依赖仅 fsnotify/fsevents 一个）、tmux 3.x、launchd、Claude Code hooks（2.1.323，行为以 P0 实测为准）。

**Spec:** `docs/superpowers/specs/2026-11-22-claude-sentinel-v1.1-design.md`（本计划从它出发，执行者两份都读）

## Global Constraints

- Go module：`github.com/Golden-forest/claude-sentinel`；单二进制 `sentinel`
- 状态/配置/spool 目录 0700，文件 0600；日志脱敏（不落完整用户消息）
- 状态写入：临时文件 → fsync → 原子 rename → 目录 fsync；带版本号与单调 seq
- 一切 deadline 用 Unix epoch 持久化；唤醒/启动后重算，不信任内存 timer
- tmux 调用一律参数数组 + exec.CommandContext，禁止 shell 拼接；文本与 Enter 分离发送
- 外部依赖上限：1 个（fsnotify/fsevents）；其余全标准库
- 检测模式默认值 = v1 遗留 9 条（见 `~/.claude-auto-retry.json`），配置可覆盖

## Review Focus

1. **追加中的半行 JSON**（watch 恰好在写入间隙读文件）→ 必须进 pending buffer 等下一轮补齐，不得丢弃或报坏数据。测试钉在 Task 4。
2. **时钟回拨/时区变化**（deadline 已过但墙钟没到，或反之）→ 以 epoch 重算判到期，正负偏差均不误发。测试钉在 Task 6。
3. **spool 被符号链接替换**（hook 兜底写入路径被劫持）→ O_NOFOLLOW 语义：拒绝跟随符号链接，事件丢弃并告警。测试钉在 Task 7。
4. **PID 复用**（绑定表里的 claude pid 死后号码被新进程占用）→ 绑定校验必须同时比对进程启动时间。测试钉在 Task 8。
5. **state.json 损坏/写满** → 隔离改名 + 告警 + 从备份恢复，绝不静默当空状态。测试钉在 Task 6。

---

### Task 1: P0-3 实测——SessionStart 钩子能否拿到 TMUX 身份

**Files:**
- Create: `p0/sessionstart-probe.sh`（临时探针，Task 12 转正后删除）
- Output: `p0/results/sessionstart-*.json`

**Interfaces:** 无代码接口；产出决定 Task 8 绑定表的数据来源。

- [ ] **Step 1:** 写探针脚本：stdin JSON 落盘到 `p0/results/sessionstart-$(date +%s).json`，同文件追加环境变量 `TMUX`、`TMUX_PANE`、`CLAUDE_PROJECT_DIR`
- [ ] **Step 2:** 把探针注册为 settings.json 的 SessionStart hook（手动加，结构化合并，不破坏现有 hooks）
- [ ] **Step 3:** 在 tmux 里新开一个 claude 会话，验证落盘文件里同时有 session_id 与 TMUX_PANE
- [ ] **Step 4:** 结果回填 spec §7 与本计划 Task 8 假设；`git commit`

### Task 2: P0-1/2/4 布控——StopFailure 与 Notification 探针就位等真实额度事件

**Files:**
- Create: `p0/stopfailure-probe.sh`、`p0/notification-probe.sh`
- Output: `p0/results/stopfailure-*.json`、`notification-*.json`

**Interfaces:** 无代码接口；P0-1 结果决定 Task 5 的 matcher 与 error 类型映射。

- [ ] **Step 1:** 两个探针脚本：stdin 全文 + 时间戳落盘；注册为 StopFailure（matcher 留空=全匹配）与 Notification 钩子
- [ ] **Step 2:** 用 `claude -p` 触发一个注定失败的请求（如无效模型名）验证 StopFailure 探针能落盘、看 error 字段形状
- [ ] **Step 3:** 真实智谱限额 429 发生时（被动等待，不烧额度）捕获：error 类型确切值 + jsonl 是否含 quotaLimits.resetsAt + 时延 → 回填 spec §5
- [ ] **Step 4:** `quota_auto_resume_*` 是否在第三方端点出现（预期不出现）→ 回填 spec §6.6；`git commit`

### Task 3: 脚手架 + patterns/classify 基础

**Files:**
- Create: `go.mod`、`cmd/sentinel/main.go`、`internal/classify/classify.go`、`internal/classify/classify_test.go`
- Test: `classify_test.go`

**Interfaces:**
- Produces: `type Incident struct{ SessionID, TranscriptPath string; ResetAt int64; Source string; Pattern string }`；`func ClassifyError(errType string, jline []byte) (*Incident, bool)`；`func ParseResetAt(jline []byte) (int64, bool)`（优先级：quotaLimits.resetsAt > 带时区明文 > 中文正则+合理性校验；解析失败返回 false）

- [ ] **Step 1:** 失败测试：`TestClassifyError_StructuredQuota`（含 resetsAt 的行 → Incident 且 ResetAt=该 epoch）、`TestClassifyError_RegexFallback`（今晚真实智谱报错样本 → 命中且时间通过校验）、`TestParseResetAt_RejectsAmbiguous`（"3pm" 无日期 → false）
- [ ] **Step 2:** `go test ./internal/classify/` 确认 FAIL（未实现）
- [ ] **Step 3:** 实现 classify（9 条默认模式烧入 `DefaultPatterns`；合理性校验：不早于 now-5min、不晚于 now+7d）
- [ ] **Step 4:** 测试 PASS；`git commit -m "feat: classify 结构化限额识别"`

### Task 4: transcript cursor 读取器

**Files:**
- Create: `internal/transcript/cursor.go`、`internal/transcript/cursor_test.go`

**Interfaces:**
- Consumes: 无
- Produces: `type Cursor struct{...}`；`func NewCursor(path string) (*Cursor, error)`；`func (c *Cursor) Poll() ([]byte, error)`（返回自上次调用后追加的完整行；半行进 pending buffer）；`func (c *Cursor) Checkpoint() (dev, ino uint64, off int64)`；`func RestoreCursor(dev, ino uint64, off int64, path string) (*Cursor, error)`（inode 变了→重扫全文件）

- [ ] **Step 1:** 失败测试：`TestPoll_HalfLine`（写半行 → Poll 空且无错 → 补齐换行 → Poll 返回整行）、`TestRestore_InodeChanged`、`TestPoll_Truncated`（文件变短 → 重扫）
- [ ] **Step 2:** 跑测试确认 FAIL
- [ ] **Step 3:** 实现（os.Stat 取 dev/ino；read 从 off 到 EOF；按 \n 切分，尾段无 \n 存 pending）
- [ ] **Step 4:** 测试 PASS；commit

### Task 5: 状态机 + 原子持久化

**Files:**
- Create: `internal/machine/machine.go`、`internal/machine/store.go`、`internal/machine/*_test.go`

**Interfaces:**
- Consumes: Task 3 的 `Incident`
- Produces: 状态枚举 `DETECTED/CLASSIFIED/SCHEDULED/DUE/VERIFYING/RESOLVED/CANCELLED/NEEDS_REVIEW/SENT_UNCONFIRMED`；`type Store struct{...}`；`func OpenStore(path string) (*Store, error)`；`func (s *Store) Update(inc *Incident, to State) error`（flock + tmp/fsync/rename/dirfsync + seq++）；`func (s *Store) Active() []*Incident`；损坏文件 → 自动隔离为 `state.json.quarantine-<ts>` 并从 `state.json.bak` 恢复，都坏则返回 error（不静默）

- [ ] **Step 1:** 失败测试：`TestUpdate_CrashBetweenRename`（注入：rename 前杀 → 旧状态完整）、`TestStore_CorruptQuarantines`（写垃圾字节 → OpenStore 返回隔离错误且旧文件已改名）、`TestDeadline_EpochRecompute`（deadline 判定只看 epoch，mock 墙钟回拨 ±2h 不影响）
- [ ] **Step 2:** 确认 FAIL → 实现 → PASS → commit

### Task 6: eventbus——socket 服务端 + spool 兜底

**Files:**
- Create: `internal/eventbus/bus.go`、`internal/eventbus/spool.go`、`*_test.go`

**Interfaces:**
- Produces: `func Serve(ctx context.Context, sockPath string, out chan<- Event) error`；`func SpoolWrite(dir string, e Event) error`（0700 目录、拒绝符号链接前缀、原子 rename）；`func SpoolImport(dir string, out chan<- Event) (n int, err error)`（导入后删除 + 按 event_id 去重）；`type Event struct{ ID, Kind, SessionID, TranscriptPath string; Payload []byte; Ts int64 }`

- [ ] **Step 1:** 失败测试：`TestSpool_RejectsSymlinkDir`、`TestSpoolImport_Dedups`（同一 ID 写两次只出一次）、`TestServe_RoundTrip`（客户端 conn 写 JSON → out 收到）
- [ ] **Step 2:** FAIL → 实现 → PASS → commit

### Task 7: binding——会话绑定表

**Files:**
- Create: `internal/binding/binding.go`、`*_test.go`

**Interfaces:**
- Consumes: Task 1 实测确认的 SessionStart 载荷字段
- Produces: `type Binding struct{ SessionID, TmuxSocket, PaneID string; Pid int; StartedAt int64 }`；`func (t *Table) Put/Get/Invalidate(...)`；`func (b *Binding) StillValid() bool`（pid 存活 + `ps -o lstart` 启动时间一致 + pane 存在且前台进程组是它）

- [ ] **Step 1:** 失败测试：`TestStillValid_PidReuse`（伪造 pid 被新进程复用、启动时间不符 → false）、`TestInvalidate_OnSessionEnd`
- [ ] **Step 2:** FAIL → 实现 → PASS → commit

### Task 8: guard——发送前核验链

**Files:**
- Create: `internal/guard/guard.go`、`*_test.go`

**Interfaces:**
- Consumes: Task 5 `Store.Active()`、Task 7 `Binding.StillValid()`
- Produces: `func Check(inc *Incident, b *Binding, evidence Evidence) Verdict`（枚举 `PROCEED/RESOLVED/CANCELLED/NEEDS_REVIEW`）；`Evidence` 含：nativeResumed bool、newerTranscriptActivity bool、paneInCopyMode bool、resetAtPassed bool

- [ ] **Step 1:** 失败测试：七道闸门各一条（任一 false → 非 PROCEED）；`TestCheck_NoBinding_NeedsReview`（无绑定绝不猜）
- [ ] **Step 2:** FAIL → 实现 → PASS → commit

### Task 9: executor——tmux 受控执行

**Files:**
- Create: `internal/executor/tmux.go`、`*_test.go`

**Interfaces:**
- Consumes: Task 8 verdict=PROCEED
- Produces: `func SendContinue(socket, paneID, text string) error`（`send-keys -t %N -l` 打文本 + `send-keys -t %N Enter`，两步分离）；`func PaneInfo(socket, paneID string) (PaneInfo, error)`

- [ ] **Step 1:** 失败测试（对真实临时 tmux server：`tmux -L test`）：`TestSendContinue_TextEnterSeparated`（收到的键序：字面文本 + Enter 两个独立事件）
- [ ] **Step 2:** FAIL → 实现 → PASS → commit

### Task 10: daemon——Router/调度器/装配

**Files:**
- Create: `cmd/sentinel/watch.go`、`internal/daemon/daemon.go`、`*_test.go`

**Interfaces:**
- Consumes: Task 3-9 全部
- Produces: `func Run(ctx context.Context, cfg Config) error`——装配 eventbus 消费 → classify → 状态机 → 定时唤醒（epoch 重算）→ guard → executor；日志按天落 `~/.local/state/claude-sentinel/logs/`

- [ ] **Step 1:** 集成测试：注入合成限额事件（fixture transcript + 加速时钟 env）→ 断言对测试 tmux server 的 pane 收到 continue，状态机走完 DETECTED→…→RESOLVED
- [ ] **Step 2:** FAIL → 实现 → PASS → commit

### Task 11: fswatch——FSEvents 补偿源

**Files:**
- Create: `internal/fswatch/fswatch.go`、`*_test.go`

**Interfaces:**
- Produces: `func Watch(ctx context.Context, root string, out chan<- string) error`（变更文件路径；处理 MustScanSubDirs 类标记 → 发重扫信号；接入 Task 4 cursor 消费）

- [ ] **Step 1:** 失败测试：临时目录追加 jsonl 行 → out 收到路径 → cursor Poll 补齐（与 Task 4 联测）
- [ ] **Step 2:** FAIL → 实现 → PASS → commit

### Task 12: install/uninstall + 钩子注册 + launchd

**Files:**
- Create: `cmd/sentinel/install.go`、`internal/svc/launchd.go`、`internal/hooks/settings.go`、`*_test.go`

**Interfaces:**
- Produces: `func MergeHook(settingsPath string, h HookEntry) error`（结构化 JSON 合并，保留他人条目，自有条目带 `"sentinel":true` 标记）；`func Install LaunchAgent(...) / Uninstall(...)`；`sentinel install` 全流程；`sentinel uninstall` 只删自有条目

- [ ] **Step 1:** 失败测试：`TestMergeHook_PreservesForeign`、`TestUninstall_RemovesOnlyOwn`；`TestSentinelHook_ExitsFast`（探针 <2s）
- [ ] **Step 2:** FAIL → 实现 → PASS → commit（转正 P0 探针为正式 `sentinel hook` 后删除临时探针）

### Task 13: status / logs / test-fire

**Files:**
- Create: `cmd/sentinel/status.go`、`cmd/sentinel/testfire.go`

- [ ] **Step 1:** `sentinel status` 输出 incident/绑定/进程状态；`logs` tail 当天；`test-fire` 用隔离 fixture + 专用 tmux pane + `SENTINEL_CLOCK_SCALE` 加速时钟走全链
- [ ] **Step 2:** 手跑 test-fire 验证 → commit

### Task 14: 测试矩阵（验收 §11 的 1/2/3/5/6）

**Files:**
- Create: `test/matrix/matrix_test.go`

- [ ] **Step 1:** 同项目 3 pane × 100 合成事故 → 0 误发；双源同发 → 1 incident；三个崩溃时点注入 → 无重复注入；epoch 回拨 ±2h → 不误发；认证/billing/普通 429 fixture → 不触发
- [ ] **Step 2:** 全绿 → commit

### Task 15: 切换上线与观察

- [ ] **Step 1:** `claude-auto-retry uninstall` → `sentinel install` → `sentinel status` 全绿
- [ ] **Step 2:** 观察清单挂 24h：RSS、CPU、日志；下次真实智谱限额阵亡完成终验（§11 第 7 条）
- [ ] **Step 3:** 结果回填 spec 验收表；更新 `~/me/cli-tools.md`；commit tag `v0.1.0`

---

**执行方式**：用户已选 native（本会话直接执行，从 Task 1 开干）。P0-1 的真实额度事件为被动等待项，不阻塞 Task 3 起的编码（classify 的正则降级层不依赖它）。
