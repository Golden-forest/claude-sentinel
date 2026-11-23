# claude-sentinel

Resume Claude Code sessions automatically after usage-limit interruptions.

When Claude Code hits a 429, sentinel reads the quota reset time, waits until it
passes, verifies the session is still the one that died, and types `continue`
into the right tmux pane.

## Design

```
events (no polling)                daemon (single process)           action
──────────────────                 ─────────────────────             ─────────
StopFailure hook ───socket──┐
SessionStart/End ───socket──┼──▶ router → bindings → state machine → tmux send-keys
FSEvents on transcripts ────┘   (spool if down)  (7-gate guard)
```

- Hooks deliver incidents over a unix socket; a spool directory catches events
  while the daemon is down.
- FSEvents on `~/.claude/projects/**/*.jsonl` is a compensation channel for
  lost hook events.
- Each incident walks DETECTED → SCHEDULED → DUE → VERIFYING → SENT → RESOLVED.
- Seven gates stand between DUE and the keystroke: terminal state, binding
  validity, PID + start-time match, pane existence, copy-mode, reset epoch
  passed, no prior resume.
- Anything ambiguous becomes NEEDS_REVIEW. It never types into a pane it
  cannot prove is the session that died.

## Install

macOS only (launchd, FSEvents). Sessions must run inside tmux.

    go install github.com/Golden-forest/claude-sentinel/cmd/sentinel@latest

or grab a binary from
[releases](https://github.com/Golden-forest/claude-sentinel/releases), then:

    sentinel install     # merge hooks (own entries only) + launchd daemon
    sentinel status      # daemon / incidents / bindings
    sentinel uninstall   # remove own entries only

## Background

Rewrite of claude-auto-retry. That project watches the terminal screen and
runs one monitor process per session; in practice incidents get missed and
idle monitors pile up. Sentinel keeps the goal and replaces the detection
model: hook events and transcript files instead of screen polling, one
daemon instead of per-session monitors.

## Acknowledgments

Derived from
[cheapestinference/claude-auto-retry](https://github.com/cheapestinference/claude-auto-retry)
(MIT).

## License

MIT
