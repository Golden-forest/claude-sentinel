# claude-sentinel 设计文档 v1.1

- 日期：2026-11-22（v1 同日，经网页版 GPT 架构审查后修订；[审查报告](../reviews/2026-11-22-gpt-review.md)）
- 状态：待评审
- 取代：[v1 设计](2026-11-22-claude-sentinel-design.md)
- 前身：[cheapestinference/claude-auto-retry](https://github.com/cheapestinference/claude-auto-retry)（MIT，v0.7.3）

## 1. 背景与定位

**要解决的问题**：Claude Code 额度耗尽（429/限额类错误）后会话停滞，需自动等待重置并续跑。

**关键环境事实（决定本项目存在意义）**：

1. Claude Code v2.1.234+ 原生支持订阅额度重置后自动续跑，但**只认 Anthropic 官方限额**。
2. 本机主力通道是**第三方端点（智谱 GLM，open.bigmodel.cn）**。实证（2026-11-20 13:57）：
   智谱 5 小时限额 429 阵亡，原生机制未介入，会话停滞约 6 小时直至人工敲「继续」。
3. 原版 claude-auto-retry 为屏幕轮询架构，实测漏检该场景（瞬态横幅 + 钩子匹配串不含
   限额类错误，双通道同时失守，见 2026-11-20 排查记录）。

**定位（C 路线）**：先在本机环境（macOS + 智谱端点 + tmux）做到极致可靠，内核按
Source → Router → Action 接口组织留产品化口子。

**设计哲学（自 v1.1 起明确）**：准确识别、正确绑定、安全恢复；**不能确认时绝不操作**。
目标不是「零漏检、必定续跑」（第三方无事务式会话恢复 API，不可证明），而是
「每一次自动操作都有据、可审计、可回溯，不确定就退位给人工」。

## 2. 目标 / 非目标

**目标**

1. 智谱端点额度阵亡的可靠自愈：事件捕获 → 结构化解析 → 定时 → 安全续跑
2. native-first：官方订阅限额场景下，原生续跑优先，sentinel 订阅原生通知并退让
3. 空闲近零开销：无轮询、无常驻监视器大军，单一守护进程
4. 崩溃安全：任何时点被杀，恢复后不重复、不丢失、不误发
5. 多会话安全：同项目多 pane 情况下绝不敲错窗口

**非目标（v1.1 明确不做）**

- 持续 overload/529 退避（Claude Code 自身会重试；将来作为新规则插入）
- 「写完功能主动 review」等开发流钩子（独立子项目）
- Linux/systemd 适配（launchd 逻辑隔离在 ServiceManager 接口后面）
- SQLite/事务日志（v1.1 用「临时文件 + fsync + 原子 rename」的 JSON 状态；量级不需要数据库）

## 3. P0 前置实测（写代码之前，用本机智谱环境完成）

GPT 审查引用的是官方文档；第三方端点行为必须实测。四项全部留档（脚本+原始输出）：

| # | 验证项 | 方法 | 影响 |
|---|---|---|---|
| P0-1 | 智谱限额 429 时 StopFailure 是否触发、error 类型的确切取值 | 临时 hook 打印 stdin JSON + 等真实/复现额度错误 | 决定 matcher 写法（预期 `rate_limit` 类结构化值，而非自由文本） |
| P0-2 | 报错时 transcript jsonl 是否含 `quotaLimits.resetsAt`（epoch）、写入延迟 | 对比 hook 触发时刻与 jsonl 落盘时刻 | 决定重置时间主来源与补偿策略 |
| P0-3 | SessionStart 钩子能否稳定读到 TMUX/TMUX_PANE 环境并携带 session_id | 临时 SessionStart hook 落盘观察 | 决定会话绑定方案可行性 |
| P0-4 | `quota_auto_resume_*` 通知在智谱端点是否触发 | 临时 Notification hook 观察 | 预期不触发（原生机制不认第三方限额）；若触发则订阅之用于退让 |

任何一项实测与本文假设不符 → 回到设计阶段修订对应章节。

## 4. 总体架构 v1.1（单调度器 + 多事件入口）

```
事件入口（三个，全部无轮询）:
  ① StopFailure hook    → 结构化事件，写 Unix socket 给 daemon
                          （daemon 离线则原子写入 spool 目录兜底，daemon 启动时导入去重）
  ② SessionStart/End 等  → 生命周期事件，维护 session↔pane 绑定表（同走 socket/spool）
  ③ FSEvents (watch 内)  → jsonl 追加的补偿观察源（cursor 式，非尾部扫描）

sentinel watch（唯一常驻进程，launchd 管理）:
  Router       → 事件分类: 限额事故 / 原生恢复信号 / 生命周期 / 噪声
  绑定表       → session_id ↔ {tmux socket, pane_id, claude pid, 启动时间}
  单写者状态机 → DETECTED → CLASSIFIED → SCHEDULED → DUE → VERIFYING →…
  调度器       → 截止时间持久化为 Unix epoch；启动/唤醒后重算，不依赖内存 timer
  Recovery Guard → 发送前逐项核验（见 §6）
  受控执行器    → 通过全部核验才 tmux send-keys（文本与 Enter 分离、参数数组、无 shell）
```

与 v1 的关键差异：hook 不再自行派生 wait 进程、不写共享状态——只投递事件；
所有决策与调度集中在一个进程内，状态所有权唯一。

## 5. 事件分类与数据

### 5.1 限额事故的识别（优先级序）

1. **StopFailure 事件的 error 结构化类型**（如 `rate_limit`；P0-1 定确切值）——第一手，最可靠
2. **jsonl 结构化字段**：`isApiErrorMessage` + `quotaLimits.{rateLimitType, resetsAt}`（epoch）
3. **降级正则**（仅上述缺失时）：v1 的 9 条模式，匹配后仍必须拿到可信重置时间才继续

### 5.2 重置时间解析（严格优先级）

1. `quotaLimits.resetsAt` epoch（绝对时间，无时区歧义）
2. 带明确时区的明文时间
3. 中文正则（`限额将在…重置`）——须通过日期/时区合理性校验（不早于现在、不晚于 7 天）
4. **解析不出或校验不过 → NEEDS_REVIEW，不动作，只通知**

### 5.3 状态机

```
DETECTED → CLASSIFIED（确认限额类且拿到可信 reset_at）
        → SCHEDULED（持久化 deadline + incident_id）
        → DUE（epoch 到点，睡眠后唤醒即补）
        → VERIFYING（Recovery Guard 全链核验）
            ├─ 原生已恢复/用户已续 → RESOLVED
            ├─ 用户显式取消 → CANCELLED
            ├─ 绑定或状态不确定 → NEEDS_REVIEW（人工）
            └─ 全部通过 → SENT_UNCONFIRMED
                  ├─ 确认恢复（jsonl 出现后续进展）→ RESOLVED
                  ├─ 再次限额（新 reset_at）→ SCHEDULED（attempts+1）
                  └─ 状态不明 → NEEDS_REVIEW
```

- incident 为单位计 attempts，上限 6；超限 → NEEDS_REVIEW
- 用户取消为最高优先级：一旦识别（显式 Esc/取消信号或绑定表标记），永不重新激活

### 5.4 状态持久化（崩溃安全）

- `~/.local/state/claude-sentinel/state.json`：单写者（仅 daemon 写）、flock 互斥
- 写入流程：临时文件 → fsync → 原子 rename → 目录 fsync；带版本号与单调递增 seq
- **损坏处置：隔离改名 + 显式告警（日志 + 通知），绝不静默当空状态**；从最近备份恢复
- 发送侧：SENT_UNCONFIRMED 崩溃恢复后按 at-most-once 处理（宁可漏一次，不可重复敲）
- 终态记录保留 14 天后清理（防无限增长）

## 6. Recovery Guard（发送前核验链，任一不过即不发送）

按序：

1. incident 存在且未 CANCELLED/RESOLVED
2. session_id ↔ pane 绑定仍有效（SessionEnd/resume/clear 后失效）
3. tmux server 身份与 pane_id 未变；pane 存活
4. 绑定记录的 claude pid + 启动时间的进程实例仍在该 pane 前台（PID 不复用：比对启动时间）
5. pane 不在 copy-mode；无待提交的用户输入迹象（可检测的范围内）
6. 无原生恢复证据（P0-4 的通知信号 / jsonl 在 deadline 后已有新进展）
7. 重置时间已过（epoch 重算，不信任睡眠前的内存 timer）

无法唯一绑定（多 pane 竞争、绑定缺失）→ **禁止猜测，进 NEEDS_REVIEW**。v1 的
「按 mtime 选最新」逻辑废弃。

## 7. 会话绑定（H1 修复）

- SessionStart hook：读 stdin 的 `session_id` + 进程环境的 `TMUX`/`TMUX_PANE`（P0-3 验证），
  连同 claude pid、`/proc` 等价启动时刻写入绑定表
- SessionEnd hook / 用户命令迹象（resume、clear）：旧绑定失效，重新绑定
- 绑定表是 Guard 第 2 步的唯一依据；没有绑定就没有发送资格

## 8. FSEvents 补偿监听（H3 修复）

- 监听 `~/.claude/projects/` 目录树（FileEvents 标志；处理 MustScanSubDirs/ dropped 等标记的重建扫描）
- cursor 式消费：以 (device, inode, offset) 追踪每个 jsonl；半行 JSON 进 pending buffer
- FSEvents 只回答「哪些文件可能变了」；cursor 回答「处理到哪了」——不依赖事件完整性
- Go 绑定：fsnotify/fsevents（cgo）；内存预算以实测 RSS 为准，15MB 是目标不是承诺

## 9. 组件与命令

- `sentinel watch`：常驻 daemon（Router/状态机/调度器/Guard/执行器全在此进程）
- `sentinel hook <event>`：全部 Claude Code 钩子的入口（socket 投递 + spool 兜底），<2s 退出
- `sentinel install / uninstall`：结构化合并 settings.json（保留他人 hooks，只动自己的带标记条目）；
  launchd plist（绝对路径、SIGTERM 优雅退出持久化检查点）；卸载只移除自己的条目
- `sentinel status / logs`：incident 列表、绑定表、状态机位置；日志脱敏（不落完整用户消息，只记
  错误类型、路径哈希、incident_id）
- `sentinel test-fire`：隔离 fixture + 专用 tmux pane + 模拟时钟，**绝不触碰真实 transcript**
- 权限：状态/配置目录 0700，文件 0600，spool 防符号链接替换

## 10. 工程形态

```
~/Projects/claude-sentinel/
  cmd/sentinel/
  internal/
    eventbus/     socket 服务端 + spool 导入
    classify/     5.1/5.2 的分类与时间解析
    binding/      绑定表
    machine/      状态机 + 原子持久化
    guard/        Recovery Guard
    executor/     tmux 受控执行
    fswatch/      FSEvents 补偿源
    svc/          ServiceManager 接口（launchd 实现）
```

工作量诚实估算（v1 低估了）：**~1000-1500 行 Go + 测试**，含崩溃注入与多会话测试矩阵。

## 11. 验收标准（GPT 六门 + v1 两条）

1. 多会话安全：同项目 ≥3 个 Claude pane，100 次模拟事故，0 次误发其他 pane
2. 双源去重：hook 与 FSEvents 同时触发，始终只产生一个有效 incident
3. 崩溃一致性：在状态写入/发送前/发送后三个时点强杀进程，均不产生重复注入
4. 原生兼容：原生恢复成功的会话，sentinel 不再发送（官方端点场景）
5. 睡眠恢复：睡眠跨重置、唤醒、重登录后按安全策略处理（不误发不漏调度）
6. 错误隔离：认证失败 / billing error / 普通 API 429 / 后台辅助查询失败，均不触发续跑
7. 真实智谱限额阵亡一次：全链自动完成，无人工「继续」
8. 空闲 24h：RSS ≤15MB（实测口径），CPU 累计 <1s

## 12. 切换与回滚

同 v1：`claude-auto-retry uninstall` → `sentinel install`；反向五分钟。另：v1 期间改过的
StopFailure matcher（`…|rate|limit|429|…`）在 P0-1 后按结构化类型收敛。

## 13. 致谢

同 v1。本版架构修订吸收了 GPT 审查报告（2026-11-22）的高严重度意见 H1-H8 与 v1.1 架构提案。

## 14. 开放问题

- P0 四项实测结果回填后确认 §5.1 matcher 确切写法与 §5.2 主来源
- 项目名暂定 claude-sentinel
