claude-sentinel 系统架构审查报告
总体裁决：修改后可执行
当前设计不建议直接实施

审查结论：技术路线基本成立，但尚未形成可靠的无人值守续跑系统。 问题不在 Go 或 FSEvents 是否可用，而在故障事件语义、会话身份绑定、恢复控制权和状态事务的一致性上。

尤其需要关注一个会改变产品定位的最新事实。

一、首先必须纠正的架构前提
1. Claude Code 已经原生支持额度重置后自动续跑
高严重程度 · 产品定位前提变化

经核对 Claude Code 官方文档，自 v2.1.234 起，交互式订阅会话默认支持：

达到使用额度限制后，保持会话打开并等待额度重置。

重置后自动继续当前任务，而非简单重发上一条消息。

在某些额度提前恢复的情况下提前继续。

连续自动续跑达到三次后停止，避免无限循环。

电脑睡眠超过约 30 分钟且额度在睡眠期间重置时，可能要求用户按 Enter 才能继续。

官方文档：Wait for a usage limit to reset。
Claude Code Docs

更重要的是，你准备替代的 cheapestinference/claude-auto-retry，其当前 README 也明确承认这项原生能力已经存在。

因此，claude-sentinel 不应该再定位成“重新实现 Claude Code 的自动续跑”，而应定位成：

Claude Code 原生额度恢复机制的可靠性补丁与外部监督器。

需要解决的实际问题包括原生自动等待未触发、睡眠后等待状态失效、会话退出后恢复、多实例冲突以及跨版本异常行为。

这也意味着新系统默认应当让原生机制优先执行，外部自动续跑只作为经过确认的兜底手段。否则可能发生两个独立系统争抢同一个会话的控制权。

二、设计缺陷与逻辑漏洞
A. 高严重程度：必须修改，否则不能实施
H1. 会话与 tmux Pane 的映射没有可靠身份依据

这是整个设计中最危险的工程缺陷。

pane_current_path 只能说明工作目录，不能证明哪个 Claude Code session 正在这个 pane 中运行。

存在非常现实的情形：

同一项目开三个 tmux pane，同时运行三个 Claude 会话。

同一项目既有主工作区，也有 Git worktree。

Claude 会话启动后执行 cd，当前路径与初始路径不一致。

一个 pane 退出旧 Claude，又启动新 Claude。

/resume 在同一进程中切换了会话。

此时，依据项目路径以及 JSONL 的 mtime 选择 pane，根本没有身份保证。

尤其不能接受“多匹配时选最新活动会话”这一逻辑。它应该直接禁止。

建议建立确定性的绑定记录：

Plain text
session_id
  ├── transcript_path
  ├── tmux_socket
  ├── tmux_pane_id
  ├── claude_pid
  ├── process_start_time
  └── binding_generation

通过 SessionStart Hook 捕获 session ID，并尝试读取继承的 TMUX / TMUX_PANE 环境信息；SessionEnd、/resume、/clear 和 fork 时更新或失效绑定。

必须验证实际 tmux socket、pane 身份和进程实例仍一致。

任何时候无法唯一绑定，必须放弃自动敲键，而不是猜测。

H2. 把 StopFailure 当成可靠的主任务失败信号

Claude Code 当前官方 Hook 文档已经明确规定：

StopFailure 的 matcher 匹配的是结构化的 error 类型，不是 HTTP 状态码或屏幕文案。

你设计中的 429|usage|limit|api_error 等字符串，不是合法的结构化错误类型。虽然其中 rate 可能匹配 rate_limit，但这不是值得依赖的精确配置。

建议先仅使用：

JSON
"matcher": "rate_limit"

并将 429 的具体文案解析放在 Hook 的业务处理层。

官方还说明：

StopFailure 包含 error、error_details、last_assistant_message。

transcript_path 是公共输入字段。

Hook 不能通过退出码强制恢复失败的调用。

server_error 还包含 HTTP 529，不能与订阅额度耗尽混为一谈。

来源：Claude Code StopFailure 官方规范。
Claude Code Docs
+1

更严重的是，已有真实的误报案例。

2026 年 9 月的 Anthropic GitHub Issue #91419 报告：内部辅助查询和后台 Subagent 失败也可能触发父会话的 StopFailure，而主会话并没有真的因额度错误终止。该 Issue 于 11 月 6 日因长期未活动关闭，不代表缺陷已修复。
GitHub

因此，必须增加 turn-failure 与 background/helper-failure 的区分；无法判定时不能自动续跑。

H3. “追加 JSONL + FSEvents = 零漏检”的推论不成立

FSEvents 只能保证观察到一定范围内的文件系统变化，不能保证 Claude Code 把所有额度错误写入 JSONL。

至少存在三种无法排除的失败情况：

API 错误触发了 Hook，但对应 JSONL 尚未落盘。

出错信息只出现在内存中的 TUI 状态，没有对应的 JSONL 消息。

Claude Code 内部直接进入原生等待状态，并未形成该系统预期的失败 transcript 记录。

官方文档明确指出：transcript 异步写入，Hook 执行时可能尚未包含当前回合的最新消息。
Claude Code Docs

解决方式：Hook 的输入 JSON 本身就是第一手事件；不要为了识别一个已经结构化报告的 rate_limit，再强制依赖 transcript 出现某条字符串。

需要将系统能力分成两层：

事件已捕获：Hook 明确报告额度错误。

恢复条件已验证：进一步确认重置时间、目标会话、当前状态等。

两者不能混为一谈。

H4. 没有处理 Claude Code 原生续跑与 Sentinel 的竞争

Claude Code 原生续跑与 Sentinel 的定时任务可能在同一重置时刻同时触发。

如果 Sentinel 读取 transcript 时尚未看到新消息，就可能发送第二个 continue。

后果不一定只是重复一轮对话，也可能在另一任务正在执行时注入新提示词。

还存在一个相反的问题：用户可能早已放弃原会话，转而在另一会话中工作，但旧会话仍在等待自动恢复。

2026 年 11 月 12 日的 Anthropic Issue #109455 就报告过旧会话自动恢复后消耗额度并与新会话发生文件操作冲突的情况。这是报告案例，尚不能证明所有版本均受影响。

修改建议： 提供 native-first 策略，并把明确的用户取消行为设为最高优先级。原生恢复成功、显式取消、会话切换或无法判断是否仍在等待时，都不应该主动发送 continue。

H5. 用“后续 user/assistant 消息”判断手动续跑，幂等性不成立

JSONL 中出现新的消息，不等于用户已经恢复工作；未出现新的消息，也不等于用户尚未干预。

例如：

新 assistant 消息可能是 API 错误或后台任务产生的。

原生续跑已经开始，但 transcript 写入延迟。

用户在 TUI 输入框内写好了新命令，还没提交。

用户通过模型切换或额度购买恢复了工作。

一个自动发送的 continue 已送达，但进程刚好崩溃。

所以，现有幂等检查只有弱推断能力。

要区分三种保证：

目标

	

现有设计能否保证




不重复安排同一等待任务

	

在正确实现锁与去重后可以




不重复调用 tmux send-keys

	

不能保证




Claude 只执行一次续跑语义

	

无法仅靠外部 tmux 注入严格保证

特别是最后一项，属于不可完全消除的外部副作用不确定性。

推荐将发送前状态持久化为 SENT_UNCONFIRMED，发送后再尝试确认。崩溃恢复遇到不确定状态时采取 at-most-once 优先 策略，避免二次发送。

H6. 状态文件 flock 不等于状态事务完整

flock 仅负责互斥，并不能保证写盘成功、写入原子性，以及状态更新与外部发送命令一致。

典型崩溃窗口：

Plain text
A. 写入等待状态
B. fork/启动 wait 进程
C. 写入 wait_pid
D. 等待到期
E. tmux send-keys
F. 写入完成状态

在 B–C 间崩溃，会丢失进程追踪；在 E–F 间崩溃，可能重复敲键；如果直接覆盖 state.json，写入中断还可能产生不完整 JSON。

建议使用单写入者状态管理或 SQLite 事务型事件日志，并明确外部副作用的恢复语义。

如果继续用 JSON 文件，至少做到：临时文件写入、fsync、原子 rename、目录同步、版本号以及状态备份。

“状态文件损坏就当成空状态”必须删除。

这是典型的静默数据丢失策略，会使待恢复任务彻底消失。

H7. tmux 前台检查仅匹配 claude/node，安全性不足

进程名不能可靠证明程序身份。

Claude Code 可能采用原生二进制运行，也可能经过包装器启动。相反，node 也可能运行完全无关的命令。

需要验证：

Pane 所属 tmux server 的身份。

运行实例 PID 与启动时间。

进程祖先关系、终端前台进程组。

Pane 未进入 tmux copy-mode 或其他交互模式。

Claude Code 当前是否处于可接受文本命令的安全输入状态。

即使这些全部通过，也无法完全识别 TUI 当前是输入框还是模态对话框。

因此，tmux 发送能力在工程上可行，但“安全地向 Claude 当前输入框发送”的正确性并不由 tmux API 保证。

H8. 缺乏对未知或错误重置时间的严格拒绝机制

已经有实际证据表明，Claude Code 某些版本的 JSONL 错误对象含有：

JSON
{
  "quotaLimits": {
    "status": "rejected",
    "rateLimitType": "five_hour",
    "resetsAt": 1790175600
  }
}

这里最有价值的是 resetsAt，不是显示给用户的自然语言重置时间。

来源：Anthropic Issue #97975：结构化额度信息建议。
GitHub

建议按以下优先级获取重置时间：

已核验版本的结构化 quotaLimits.resetsAt。

存在时使用明确时区的重置时间。

其他自然语言时间仅作为有严格校验的降级方案。

无法确认时不执行自动恢复，通知人工处理。

如果只看到 3pm，却无法证明日期和时区，绝不能武断推断为当天或次日。

B. 中低严重程度：必须纳入实现计划

编号

	

严重程度

	

问题

	

处理建议




M1

	

中

	

一个故障派生一个 wait 进程，多个会话及反复失败会产生进程竞争

	

用单个调度器管理定时任务




M2

	

中

	

wait_pid 可能被操作系统复用

	

增加进程创建时间或实例标识，不用 PID 单独证明身份




M3

	

中

	

maxRetries=6 没定义统计作用域

	

明确以 incident 为单位，并设置跨 incident 的总上限




M4

	

中

	

后续同类错误是否重置等待计划不明确

	

按新的 ResetAt 更新事件，不盲目新增任务




M5

	

中

	

Zero pane 匹配后固定放弃时间，可能与长期额度等待不匹配

	

区分临时 tmux 不可用、会话消失、系统重新登录




M6

	

中

	

install 修改用户 settings.json，存在覆盖其他 Hook 的风险

	

结构化合并，保留无关设置，记录安装清单




M7

	

中

	

直接反向卸载无法处理安装后用户修改的配置

	

只移除 Sentinel 自己的条目，不还原整个旧文件




M8

	

中

	

test-fire 修改实际 transcript 可能污染会话历史

	

使用隔离 fixture、专用 tmux pane 与模拟时钟




M9

	

中

	

没有明确设置文件权限和日志脱敏策略

	

目录 0700、状态文件 0600、日志不得记录完整用户消息




M10

	

中

	

“300–500 行 Go”低估工程复杂度

	

重新估算状态机、平台适配、异常恢复和集成测试工作量




L1

	

低

	

把平台服务隔离在单个 launchd 文件里不足以完成跨平台抽象

	

定义 ServiceManager、EventWatcher 接口




L2

	

低

	

单一状态 JSON 无限增长缺少清理规则

	

增加终态保留期限和清理机制

三、五大关键技术环节可行性评估

组件

	

可行性

	

风险级别

	

审查裁决




macOS FSEventStream

	

高

	

中

	

可用，但不是可靠消息队列




launchd 常驻

	

高

	

中

	

可用，但不保证登出后持续运行




tmux send-keys

	

高

	

高

	

发送容易，安全发送困难




StopFailure Hook

	

高

	

高

	

API 正式存在，但事件语义不能无条件信任




JSONL transcript 解析

	

中高

	

高

	

可提取信息，不能当作完全可靠的实时状态

1. macOS FSEventStream

结论：技术成立，现有文件监听策略不完整。

Apple 明确说明 FSEvents 事件可能合并、丢失，事件标记要求应用在必要时递归重新扫描目录。它不是严格的每次写入通知机制。
developer.apple.com

需要落实的实现细节：

监听 ~/.claude/projects/ 目录树，而非假设 API 原生支持 **/*.jsonl 通配符。

使用文件级事件标记 kFSEventStreamCreateFlagFileEvents，并处理文件创建、修改、删除。

发现 MustScanSubDirs、UserDropped、KernelDropped、RootChanged 时执行对应范围的重建扫描。

监听建立后进行基线扫描并补齐期间变化；维护文件偏移与事件检查点。

用 (device, inode, offset) 追踪文件，而非只保存路径和 mtime。

对追加中的半行 JSON 使用 pending buffer，不能将未完整落盘的行当成坏数据。

处理文件截断、替换、重命名以及旧事件重复交付。

Go 现成库 fsnotify/fsevents 可以承担 CoreServices 绑定。需要注意它使用 cgo；若自行开发，CoreFoundation RunLoop、回调线程和 stream 生命周期都要正确管理。

建议：FSEvents 只负责提醒“哪些文件可能发生变化”，JSONL cursor 才负责“处理到哪里”。

否则防抖 500ms 后简单读取最后追加行，无法满足故障恢复要求。

2. launchd 常驻

结论：适合本地 Mac 用户代理，但不是永久执行保证。

采用 ~/Library/LaunchAgents 是正确方向。

Apple 文档说明用户登出时，会向用户代理发送 SIGTERM。因此，RunAtLoad + KeepAlive 不能被理解为机器重启、用户登出、系统睡眠期间均持续运行。
developer.apple.com

实现还必须考虑：

plist 使用绝对二进制路径和明确的环境变量。

使用正确的 launchd 用户域进行 bootstrap、bootout、kickstart。

处理服务启动失败、快速崩溃引起的限速重启。

捕获 SIGTERM，持久化检查点并优雅退出。

重新登录后恢复任务前，重新确认目标会话是否仍然存在。

建议把 watch 和定时调度器放在同一个 launchd 进程内，避免依赖大量 detached 子进程。

3. tmux send-keys 控制

结论：作为执行器可以，作为可靠的会话控制协议还不够。

tmux 提供稳定的 pane ID、当前路径、当前命令、进程 ID 和 pane 模式信息，但这些都不等于 Claude Code 自身的 session identity。
Linux manual page

推荐最后执行动作的验证顺序：

Plain text
绑定的 session_id 仍然有效？
       ↓
tmux server 与 pane 身份未改变？
       ↓
原 Claude 进程实例仍然存在？
       ↓
Pane 前台运行的确实是目标 Claude？
       ↓
没有模态界面、待输入文本或正在执行的任务？
       ↓
没有用户取消或原生恢复证据？
       ↓
全部通过才允许 send-keys

最后两项无法通过当前稳定 API 完整证明，需要设立“无法确定就不发送”的安全策略。

发送文本时应将普通文本与回车分离，避免将 retryText 当作 tmux key 名称解释；调用 tmux 应使用 exec.CommandContext 和参数数组，不通过 Shell 拼接。

另外，send-keys 返回成功只能证明 tmux 接受了命令，不能证明 Claude Code 已按预期接受并执行提示词。

4. Claude Code StopFailure Hook

结论：应作为主要故障观察源，但不建议独占恢复决策。

当前官方文档还提供了更相关的 Notification 类型：

事件

	

意义

	

Sentinel 处理




StopFailure: rate_limit

	

额度类 API 错误

	

创建候选故障




quota_auto_resume_fired

	

原生机制已恢复任务

	

取消外部恢复




quota_auto_resume_stale

	

睡眠导致原生等待失效，等待 Enter

	

进入受控兜底流程




quota_auto_resume_disabled

	

原生自动等待结束但未续跑

	

区分原因；不能直接强制续跑

这些 quota_auto_resume_* 是自 v2.1.234 起支持的官方通知类型。特别是 stale，它比扫描自然语言日志更直接对应你想解决的睡眠恢复故障。
Claude Code Docs

同时应加入 SessionStart、SessionEnd 和必要的会话活动 Hook，构建会话生命周期，而不是只监听失败。

注意：UserPromptSubmit 也可能由自动任务触发，不能简单把它当作用户人工输入的证明。

5. JSONL transcript 解析

结论：适合解析事件证据，不适合作为唯一真相源。

应该严格解析 JSON 结构，而不是对原始 JSONL 行直接运行正则表达式。

建议支持：

type、sessionId、uuid、timestamp。

isApiErrorMessage 和相关错误字段。

存在时的 quotaLimits.rateLimitType、quotaLimits.resetsAt。

消息角色、父子关系以及真实事件顺序。

Claude Code 版本差异和未知字段。

自然语言正则仅用于结构化字段缺失后的降级识别。

尤其注意：只读取尾部 50 行不等于读取最近 50 条有效会话事件。 大型 JSONL、Subagent 记录、超长单行和异步写入都可能让这种方案失效。

建议限制单行大小、总扫描字节数、解析时间，并为超限和未知格式提供可观测的错误状态，而不是直接跳过。

四、被忽略的边缘情况

以下场景应加入测试矩阵，不能只测试“正常报错一次、正常续跑一次”。

类别

	

边缘情况

	

必须验证的行为




并发

	

Hook 和 Watch 同时发现同一错误

	

只创建一个 incident




并发

	

两个 wait 同时到期

	

最多一个被授权发送




并发

	

原生自动续跑与 Sentinel 同时触发

	

Sentinel 主动退让




多会话

	

同一项目三个 Claude pane

	

不能跨会话发送




多会话

	

两个 tmux server 中存在相同 pane ID

	

必须结合 server 身份




会话切换

	

/resume、/clear、fork

	

旧绑定失效，重新绑定




会话退出

	

Claude 退出，pane 返回 Shell

	

禁止发送




用户操作

	

用户输入了尚未提交的提示词

	

不得覆盖或代替输入




用户操作

	

用户取消自动等待

	

不得重新激活




生命周期

	

重置前退出、重新登录、服务重新启动

	

重新核验状态而非直接续跑




睡眠

	

Mac 睡眠并跨越重置时刻

	

唤醒后重算到期条件




时间

	

DST 切换、系统时钟回拨、时区变化

	

以绝对 epoch 为准




时间

	

周额度重置在数天以后

	

不溢出、不错误缩短等待




文件

	

JSONL 截断、旋转或 inode 变化

	

正确重建 offset




文件

	

transcript 写入晚于 Hook

	

能补偿读取，不永久漏记




状态

	

crash 发生于 send-keys 后、状态提交前

	

不自动重复发送




状态

	

state.json 损坏、磁盘空间耗尽

	

隔离异常并报警




故障分类

	

账户认证失败、billing error、普通 API 429

	

不误判成订阅额度重置




重试

	

同一额度窗口反复报错

	

有界退避，不形成重试风暴

特别补充：Go 实现的隐藏成本

有五项需要在技术设计阶段定下来。

第一，exec 并不等于派生 detached 进程。 syscall.Exec 的语义是替换当前进程映像，而 exec.Cmd.Start 才是启动子进程。后者还涉及进程回收、stdio 重定向、退出状态与 orphan 管理。

第二，Go Timer 不应被当作可靠的跨睡眠绝对时间调度器。 统一持久化 Unix epoch；启动、恢复和唤醒后重新计算截止时间，不能仅依赖之前创建的 time.Timer。必要时监听 macOS 的睡眠/唤醒事件。

第三，Go 的内存预算应以 RSS 实测，而不是二进制大小推断。 cgo、FSEvents、Go runtime、GC 和目录索引都会增加常驻内存。15 MB 可以作为优化目标，但现在不能承诺。

第四，状态并发测试需要覆盖多进程。 go test -race 可以发现 Go 内部数据竞争，却不能证明跨进程 flock、文件替换和外部 tmux 副作用的一致性。

第五，文件路径本身属于攻击面。 状态目录和临时文件应使用限制性权限，避免符号链接替换；日志只记录错误类型、路径哈希和事故 ID，不应复制完整 transcript 内容。

五、建议的架构修订方案

我不建议继续沿用“Hook/Watch 各自派生 wait 进程”的方式。

更合适的是：单调度进程 + 多事件入口 + 持久化状态机 + 有条件执行器。

建议架构：claude-sentinel v1.1

Claude Code 事件

StopFailure · SessionStart/End · Notification

sentinel hook

解析结构化事件，快速投递到本地 Unix Socket

sentinel watch（唯一常驻进程）

事件 Router

FSEvents 补偿

单写入者状态机

定时任务调度器

持久化 incident、session→pane 绑定与执行状态

Recovery Guard

核对身份、额度、会话活动、原生续跑状态

验证成功

受控发送 tmux 命令

验证失败

停止并报告，不猜测

它有三个明显优势：

减少进程管理复杂度。 一个 Go 进程即可负责 FSEvents、Socket、状态机和多个定时任务；空闲时同样不需要轮询。

状态所有权明确。 Hook 不修改共享状态文件，只发送事件；调度器统一决定是否安排与执行任务。

容易扩展。 未来新增 Source 或 Action 时，仍可复用 Router 和状态机，保留原设计的平台化潜力。

为避免 Hook 发送事件时 daemon 恰好离线，增加一个很小的 durable inbox 作为降级路径：Hook 向受限目录原子写入事件，daemon 启动时统一导入并去重。

推荐状态机
Plain text
DETECTED
    ↓
CLASSIFIED
    ↓
SCHEDULED
    ↓
DUE
    ↓
VERIFYING
    ├── 原生已恢复 → RESOLVED
    ├── 用户已取消 → CANCELLED
    ├── 目标不确定 → NEEDS_REVIEW
    └── 条件满足 → SENT_UNCONFIRMED
                         ├── 确认恢复 → RESOLVED
                         ├── 再次限额 → SCHEDULED
                         └── 状态不明 → NEEDS_REVIEW

关键是任何需要外部副作用的转换都要有明确、可恢复的持久化语义。

六、重新制定验收标准

现有的四条验收标准不足以证明系统可以安全无人值守运行。

建议新增六项强制验收门槛：

验收项目

	

合格条件




多会话安全

	

同项目至少 3 个 Claude pane，100 次模拟事故，0 次误发其他 pane




双事件去重

	

Hook 和 Watch 同时触发，始终只有一个有效 incident




崩溃一致性

	

在状态写入、发送前、发送后分别强制终止，均不会形成重复自动注入




原生恢复兼容

	

原生自动恢复成功后，Sentinel 不能再发送额外的 continue




睡眠恢复

	

睡眠跨重置、唤醒、重新登录均能按安全策略处理




错误隔离

	

认证错误、billing error、普通 429、后台辅助查询失败均不触发不恰当的续跑

此外，真实使用前必须先在当前安装版本完成三个 API 实测：

StopFailure 在真实订阅额度失败时是否触发，以及实际字段。

出错时 transcript 是否写入结构化 quotaLimits，写入有多大延迟。

SessionStart Hook 能否可靠捕获 tmux 身份，并与实际 pane 绑定。

这三个属于设计依赖的前置技术验证，不应留到编码完成后才确认。

七、实施优先级裁决

优先级

	

具体任务

	

裁决




P0

	

实测原生续跑、StopFailure、quota 通知及 JSONL 结构

	

必须先做




P0

	

建立 session ID ↔ tmux pane 的可靠绑定

	

必须先做




P0

	

实现可恢复状态机、执行前 Guard、取消语义

	

必须先做




P1

	

单进程调度器与 launchd 恢复

	

首版必须具备




P1

	

结构化额度解析与重试预算

	

首版必须具备




P1

	

多会话并发、崩溃注入、睡眠测试

	

发布前必须通过




P2

	

FSEvents 增强型补偿监听

	

可在 Hook 主链路跑通后增加




P2

	

平台化 Source/Action 插件系统

	

保留接口，暂不扩展实现

八、总体可行性裁决
修改后可执行

当前版本：不批准直接进入生产实现

最终技术判断

从 macOS 能力、Go 工具链、Claude Code Hook 接口和 tmux 控制能力来看，不存在无法跨越的技术障碍。但现有设计存在多个会导致误续跑、重复命令和静默漏检的核心缺陷，尚不满足无人值守工具应有的安全要求。

我最赞成保留的设计决策： 不再依赖 5 秒一次的屏幕轮询作为主要故障检测源，而采用事件驱动与持久化状态。这是正确的工程方向。

我最反对保留的三项设计决策：

多 pane 匹配时通过项目路径和最新 mtime 猜测目标。

用尾部 50 行 JSONL 判断是否已经恢复，把它当作可靠幂等保证。

Hook/Watch 直接派生多个 wait 进程，各自等待后尝试控制 tmux。

这三个问题必须修改。

另外，已有一个尚未关闭的 Anthropic Issue #91535，报告原生续跑在 detached tmux 中不触发。该报告发生在 Linux、Claude Code 2.1.236，不足以证明当前 macOS 版本存在同样缺陷，但说明外部监督器确实存在潜在价值。

因此，我的最终建议是：

做 claude-sentinel，但改变目标：不是追求“检测到错误后必定发送 continue”，而是追求“准确识别、正确绑定、安全恢复、不能确认时绝不误操作”。

在这个标准下，项目完全值得实施；至于“零漏检、绝不重复执行、任意情况下自动续跑”，由于 Claude Code 没有向第三方提供完整的事务式会话恢复 API，目前不能作为可证明的绝对承诺。

一句话定案：保留 Go + Hook + FSEvents + launchd + tmux 技术栈；重写会话身份管理、状态机和执行器；将原生续跑设为第一优先级。