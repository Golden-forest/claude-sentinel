#!/bin/bash
OUT="/Users/hl/Projects/claude-sentinel/p0/results/notification-$(date +%Y%m%d-%H%M%S).json"
{ echo '---STDIN---'; cat; echo '---ENV---'; env | grep -E '^(TMUX_PANE|CLAUDE)' || true; } > "$OUT" 2>&1
exit 0
