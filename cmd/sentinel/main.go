// sentinel：Claude Code 额度自愈哨兵。单二进制多姿态：watch / hook / install / uninstall / status / test-fire。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/Golden-forest/claude-sentinel/internal/binding"
	"github.com/Golden-forest/claude-sentinel/internal/daemon"
	"github.com/Golden-forest/claude-sentinel/internal/eventbus"
	"github.com/Golden-forest/claude-sentinel/internal/hooks"
	"github.com/Golden-forest/claude-sentinel/internal/machine"
	"github.com/Golden-forest/claude-sentinel/internal/svc"
)

const (
	label = "com.hl.claude-sentinel.watch"

	hookMatcher = "rate_limit|overloaded|server_error|api_error"
)

func stateDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "claude-sentinel")
}

func defaults() daemon.Config {
	d := stateDir()
	return daemon.Config{
		SocketPath: filepath.Join(d, "bus.sock"),
		SpoolDir:   filepath.Join(d, "spool"),
		StatePath:  filepath.Join(d, "state.json"),
		RetryText:  "continue",
		Tick:       5 * time.Second,
	}
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "watch":
		cfg := defaults()
		cfg.WatchRoot = watchRoot()
		os.MkdirAll(cfg.SpoolDir, 0700)
		os.MkdirAll(filepath.Join(stateDir(), "logs"), 0700)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT) // I3：优雅退出
		go func() {
			<-sig
			cancel()
		}()
		if err := daemon.Run(ctx, cfg); err != nil {
			fmt.Fprintln(os.Stderr, "sentinel watch:", err)
			os.Exit(1)
		}
	case "hook":
		hookMain(os.Args[2])
	case "install":
		installMain()
	case "uninstall":
		uninstallMain()
	case "status":
		statusMain()
	case "test-fire":
		testFireMain()
	default:
		usage()
	}
}

func usage() {
	fmt.Println("用法: sentinel <watch|hook EVENT|install|uninstall|status|test-fire>")
}

// hookMain：钩子入口。stdin JSON + env → socket；daemon 不在则落 spool。<2s 退出。
func hookMain(event string) {
	if event == "" {
		event = "stop_failure"
	}
	kind := map[string]string{
		"stop_failure": "stop_failure", "session_start": "session_start",
		"session_end": "session_end", "notification": "notification",
	}[event]
	if kind == "" {
		os.Exit(0)
	}
	stdin, _ := io.ReadAll(os.Stdin)
	var base struct {
		SessionID      string `json:"session_id"`
		TranscriptPath string `json:"transcript_path"`
	}
	_ = json.Unmarshal(stdin, &base)
	ev := eventbus.Event{
		ID:             kind + ":" + base.SessionID + ":" + strconv.FormatInt(time.Now().UnixNano(), 10),
		Kind:           kind,
		SessionID:      base.SessionID,
		TranscriptPath: base.TranscriptPath,
		Ts:             time.Now().Unix(),
	}
	switch kind {
	case "stop_failure", "notification":
		ev.Payload = json.RawMessage(stdin) // 原文转发，daemon 侧解析 error 等字段
	case "session_start":
		payload := map[string]any{
			"tmux_socket": daemon.ParseTmuxSocket(os.Getenv("TMUX")), // C2：拆掉 ,pid,session 后缀
			"pane_id":     os.Getenv("TMUX_PANE"),
		}
		pid := 0
		if v, err := strconv.Atoi(os.Getenv("CLAUDE_PID")); err == nil {
			pid = v
		} else {
			pid = os.Getppid() // I1：钩子由 claude 直接 spawn 时 PPID 即 claude
		}
		if pid > 0 {
			payload["pid"] = pid
			if st, err := binding.ProcStartTime(pid); err == nil {
				payload["started_at"] = st
			}
		}
		ev.Payload, _ = json.Marshal(payload)
	}
	// socket 优先
	conn, err := net.DialTimeout("unix", defaults().SocketPath, time.Second)
	if err == nil {
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(1500 * time.Millisecond))
		b, _ := json.Marshal(ev)
		if _, err := conn.Write(append(b, '\n')); err == nil {
			return
		}
	}
	cfg := defaults()
	os.MkdirAll(cfg.SpoolDir, 0700)
	_ = eventbus.SpoolWrite(cfg.SpoolDir, ev)
}

func binPath() string {
	if p, err := os.Executable(); err == nil {
		if abs, err2 := filepath.EvalSymlinks(p); err2 == nil {
			return abs
		}
		return p
	}
	return "/usr/local/bin/sentinel"
}

func settingsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "settings.json")
}

func installMain() {
	bin := binPath()
	d := stateDir()
	os.MkdirAll(d, 0700)
	os.MkdirAll(filepath.Join(d, "logs"), 0700)
	sp := settingsPath()
	for _, e := range []hooks.Entry{
		{Event: "StopFailure", Matcher: hookMatcher, Command: bin + " hook stop_failure"},
		{Event: "SessionStart", Matcher: "", Command: bin + " hook session_start"},
		{Event: "SessionEnd", Matcher: "", Command: bin + " hook session_end"},
	} {
		if err := hooks.MergeHook(sp, e); err != nil {
			fmt.Fprintln(os.Stderr, "merge hook:", err)
			os.Exit(1)
		}
	}
	plist, err := svc.WritePlist(filepath.Join(homeLaunchAgents()), label, bin,
		filepath.Join(d, "logs", "watch.log"), filepath.Join(d, "logs", "watch.log"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "plist:", err)
		os.Exit(1)
	}
	if err := svc.Bootstrap(plist); err != nil {
		fmt.Fprintln(os.Stderr, "launchd:", err)
		os.Exit(1)
	}
	fmt.Println("sentinel installed: hooks merged + launchd agent running")
}

func watchRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "projects")
}

func homeLaunchAgents() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents")
}

func uninstallMain() {
	_ = svc.Bootout(label)
	os.Remove(filepath.Join(homeLaunchAgents(), label+".plist"))
	if err := hooks.RemoveOwnHooks(settingsPath(), binPath()+" hook"); err != nil {
		fmt.Fprintln(os.Stderr, "remove hooks:", err)
	}
	fmt.Println("sentinel uninstalled (own entries only)")
}

func statusMain() {
	cfg := defaults()
	if conn, err := net.DialTimeout("unix", cfg.SocketPath, time.Second); err == nil {
		conn.Close()
		fmt.Println("daemon: RUNNING")
	} else {
		fmt.Println("daemon: DOWN")
	}
	if n, err := countSpool(cfg.SpoolDir); err == nil && n > 0 {
		fmt.Printf("spool: %d pending\n", n)
	}
	if b, err := os.ReadFile(cfg.StatePath); err == nil {
		var st struct {
			Incidents []*machine.Incident `json:"incidents"`
		}
		if json.Unmarshal(b, &st) == nil {
			n := 0
			for _, inc := range st.Incidents {
				fmt.Printf("incident %s state=%s reset=%d (%s)\n", inc.ID, inc.State, inc.ResetAt,
					time.Unix(inc.ResetAt, 0).Format("01-02 15:04:05"))
				n++
			}
			if n == 0 {
				fmt.Println("incidents: none")
			}
		}
	}
}

func countSpool(dir string) (int, error) {
	es, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range es {
		if filepath.Ext(e.Name()) == ".json" {
			n++
		}
	}
	return n, nil
}

// test-fire：隔离 fixture + 专用 tmux pane 走全链（详见 plans Task 13）。
func testFireMain() {
	fmt.Println("test-fire: 见 go test ./internal/daemon/ -run TestDaemon_EndToEnd（真实链路验收）")
}
