#!/bin/bash
# P0-3 探针：验证 SessionStart 钩子能同时拿到 session_id（stdin JSON）与 tmux 身份（env）
OUT="/Users/hl/Projects/claude-sentinel/p0/results/sessionstart-$(date +%Y%m%d-%H%M%S).json"
{
  echo '---STDIN---'
  cat
  echo '---ENV---'
  env | grep -E '^(TMUX|TMUX_PANE|CLAUDE_PROJECT_DIR|CLAUDE_AUTO_RETRY)' || echo '(no TMUX env)'
  echo '---CWD---'
  pwd
} > "$OUT" 2>&1
exit 0
