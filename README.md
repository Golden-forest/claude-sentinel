# claude-sentinel

Claude Code 额度自愈哨兵：限额 429 阵亡 → 事件驱动检测 → 等到重置 → 安全续跑。

## 架构（v1.1）

```
事件入口(无轮询)                      常驻核心(单进程)                动作
─────────────────                    ────────────────────           ──────────
StopFailure 钩子 ──socket──┐
SessionStart/End ──socket──┼──▶ Router → 绑定表 → 状态机 → Guard ──▶ tmux send-keys
FSEvents jsonl 监听 ───────┘   (spool 兜底)   (epoch 调度)  (七道闸门)   (文本/Enter 分离)
```

- 设计文档：`docs/superpowers/specs/2026-11-22-claude-sentinel-v1.1-design.md`
- 外部审查：`docs/superpowers/reviews/2026-11-22-gpt-review.md`（GPT，修改后可执行）

## 使用

```sh
sentinel install     # 合并钩子（结构化、只增删自有条目）+ 装 launchd 守护
sentinel status      # 守护进程/事故/绑定状态
sentinel uninstall   # 只移除自有条目
```

## 致谢

前项目 [cheapestinference/claude-auto-retry](https://github.com/cheapestinference/claude-auto-retry)（MIT）。
本项目把检测源从屏幕轮询换成「钩子事件 + transcript 文件监听」，砍掉每会话监视器进程。

## License

MIT
