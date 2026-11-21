# claude-sentinel 设计文档

- 日期：2026-11-22
- 状态：待评审
- 前身：[cheapestinference/claude-auto-retry](https://github.com/cheapestinference/claude-auto-retry)（MIT，v0.7.3）

## 1. 背景与定位

2026-11-20 13:57 的实锤案例：额度 429 报错文本与原版三条模式**全部命中**，但两道防线同时漏检——
屏幕轮询（5s 一次）错过瞬态渲染的报错横幅；StopFailure 钩子匹配串 `overloaded|server_error`
不认 429 限额类失败。结论：屏幕轮询是架构级缺陷，事件驱动 + 持久化检测才是治本。

**定位（C 路线）**：先在本机环境做到极致优雅，代码按通用形状写，跑稳后走产品化。

**更远的产品愿景**（本 spec 不实现，只约束内核形状）：事件源可插拔的「AI 神经系统」——
某件事发生，AI 主动介入干对应的活。额度自愈只是第一根神经。

## 2. 目标 / 非目标

**目标**

1. 完整替代 claude-auto-retry 的「额度阵亡 → 自动续跑」，零漏检（不依赖屏幕渲染）
2. 空闲时近零开销：无轮询、无常驻监视器大军；单 watch 进程 ≤15MB
3. 内核按 Source → Router → Action 三接口组织，v1 各只有一个实现，为平台化留口
4. Go 单二进制，`sentinel` 一个命令全家桶

**非目标（v1 明确不做）**

- 平台事件集成（手机通知、定时任务等）——将来作为新 Source 插入
- 「写完功能主动 review」等开发流钩子——独立子项目，另行设计
- 持续 overload/529 的等待退避（原版另一条通道）——Claude Code 自身会重试这类瞬时错误；
  需要时作为新规则插入，v1 刻意不做
- Linux/systemd 适配——产品化阶段做（launchd 逻辑隔离在一个文件里）

## 3. 总体架构

```
cc-glm ──> claude（裸跑，无 wrapper）
              │
              ├─ [Source A] StopFailure 钩子（settings.json 注入）
              │     sentinel hook：读 stdin JSON → 扫 transcript 尾部报错 → 派生 wait → 退出
              │
              ├─ [Source B] sentinel watch 常驻进程（launchd 管理）
              │     fsevents 监听 ~/.claude/projects/**/*.jsonl → 追加行匹配报错 → 派生 wait
              │
              └─ [Action] sentinel wait 短命进程（仅事故时存在）
                    睡到 重置时刻+margin → 定位 tmux 窗口 → 前台安全检查
                    → 确认无人手动续过 → tmux send-keys "continue" Enter → 自灭
```

优雅性的来源：事故发生前，系统里只有 Source B 一个 ~10MB 进程；Source A 是被 Claude Code
被动叫醒的，零成本；Action 只在真出事时活几分钟。

## 4. 组件设计

### 4.1 `sentinel hook`

- Claude Code 停止失败时被调用，stdin 收 JSON（含 `session_id`、`transcript_path`；字段以实现时实测为准）
- 从 transcript_path 指向的 jsonl 读尾部 N 行（默认 50），匹配报错模式
- 命中：解析重置时间 → 写状态（去重签名）→ `exec` 派生 detached `sentinel wait` → 立即退出
- 未命中：静默退出（钩子绝不能阻塞 Claude Code，整体超时预算 <2s）

### 4.2 `sentinel watch`（唯一常驻进程）

- launchd Label：`com.hl.claude-sentinel.watch`，RunAtLoad + KeepAlive（崩溃自愈）
- FSEventStream 递归监听 `~/.claude/projects/`，防抖 500ms 后读追加行
- 匹配逻辑与 hook 完全共用（同一个 `internal/patterns`）
- 启动时读状态文件，恢复「等待中但 wait 进程已死」的事故（重新派生 wait）
- 日志轮转：按天一个文件，保留 14 天

### 4.3 `sentinel wait`（短命行动进程）

时序：

1. 睡到 `重置时刻 + marginSeconds`（默认 180s）
2. 幂等检查：读 jsonl 尾部，报错行之后若有新的 user/assistant 消息（用户已手动续）→ 记日志退场
3. 会话→窗口映射：jsonl 项目路径 ↔ `tmux list-panes -F '#{pane_id} #{pane_current_path}'`
   - 唯一匹配 → 用之；多匹配 → 选 jsonl mtime 最新的活动会话；零匹配（tmux 没开）→ 每 60s 重试，上限 10 分钟后放弃记日志
4. 前台安全检查：目标 pane 的前台进程必须是 claude/node（移植原版 pane-key 逻辑，防止往 shell 里乱敲）
5. `tmux send-keys -t <pane> "continue" Enter`（retryText 可配）
6. 若再次检测到同类报错（续早了）→ 重新解析重置时间，重新等待，计入 attempts，超过 maxRetries（默认 6）放弃并记日志

### 4.4 `sentinel install / uninstall / status / logs / test-fire`

- `install`：写 StopFailure 钩子到 `~/.claude/settings.json`（匹配串 `overloaded|server_error|rate|limit|429|api_error|usage`）；装 launchd plist；打印收尾说明
- `uninstall`：全部反向移除
- `status`：常驻进程状态、活动等待、最近事故
- `logs`：tail 当天日志
- `test-fire`：向指定（或合成）transcript 注入一条合成报错，以加速时钟走完 检测→等待→敲键 全链路，用于验收演示

## 5. 数据与配置

- 配置 `~/.config/claude-sentinel/config.json`：`patterns`（默认烧入已加宽的 9 条）、`marginSeconds:180`、`maxRetries:6`、`retryText:"continue"`
- 状态 `~/.local/state/claude-sentinel/state.json`：活动等待数组（session、签名、until、attempts、wait_pid），hook/watch 双通道去重与崩溃恢复共用
- 日志 `~/.local/state/claude-sentinel/logs/YYYY-MM-DD.log`
- 事件内部结构：`{Source, SessionID, TranscriptPath, MatchedLine, ResetAt}`（Router 接口的载荷）

## 6. 错误处理

- Mac 睡眠导致 wait 超时漂移：醒来立即补执行（Go timer 单调钟在睡眠中暂停，晚醒可接受）
- tmux server 不在：见 4.3 的重试上限
- 状态文件损坏：解析失败按空状态处理，宁可漏一次也不瘫痪
- 双通道竞争：状态文件写入用 flock；后到者见到同签名直接跳过
- send-keys 失败：记日志，wait 退出（不无限重试敲键）

## 7. 测试计划

- 表驱动单测：模式匹配（含今晚真实报错样本）、重置时间解析（中英文/跨日）、幂等判定
- `watch --once <file>` 模式：对 fixture jsonl 单次扫描，便于集成测试
- `test-fire` 端到端演示：真实 tmux + 合成事故，验证全链路
- 验收即「② 的四条标准」

## 8. 验收标准

1. `test-fire` 全链路通过（检测 → 等待 → 定位窗口 → 敲键）
2. 真实额度阵亡一次：日志有检测记录，重置后自动续跑，全程无需人工「继续」
3. 空闲 24h：常驻内存 ≤15MB，CPU 累计 <1s
4. 手动续过的会话绝不重复敲键（幂等验证）

## 9. 工程形态

```
~/Projects/claude-sentinel/
  go.mod                      module github.com/Golden-forest/claude-sentinel
  cmd/sentinel/               main.go + 子命令
  internal/
    patterns/                 模式与重置时间解析
    transcript/               jsonl 尾部读取与解析
    tmuxctl/                  pane 枚举、前台检查、send-keys
    state/                    状态文件与 flock
    config/
    logging/
    sources/                  hook + watch（Source 接口的两个实现）
    actions/                  wait（Action 接口的唯一实现）
  launchd/                    plist 模板
```

规模预估：300–500 行 Go + 测试。Go 版本：本机 toolchain。

## 10. 切换与回滚

1. `claude-auto-retry uninstall`（卸钩子、卸 launchd、删 .zshrc 注入段；`~/.claude-auto-retry.json` 留档）
2. `go build && sentinel install`
3. 回滚：`sentinel uninstall && npm i -g claude-auto-retry && claude-auto-retry install`（五分钟内双向可退）

## 11. 致谢

架构参照 cheapestinference/claude-auto-retry（MIT）：StopFailure 钩子接缝、前台安全检查、
菜单导航思路。README 中署名致敬；本项目的差异是检测源从屏幕轮询换成事件 + 持久化 transcript。

## 12. 开放问题

- 项目名暂定 `claude-sentinel`，发布前可改（module path 一并改）
- StopFailure stdin JSON 的确切字段实现时实测确认（不影响架构）
