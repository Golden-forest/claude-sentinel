// Package svc：launchd 服务管理（plist 生成 + bootstrap/bootout）。
package svc

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const plistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>watch</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>ProcessType</key>
    <string>Background</string>
    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
    </dict>
    <key>StandardOutPath</key>
    <string>%s</string>
    <key>StandardErrorPath</key>
    <string>%s</string>
</dict>
</plist>
`

// WritePlist 生成 plist 到目标路径，返回该路径。
func WritePlist(dir, label, binPath, logOut, logErr string) (string, error) {
	name := label + ".plist"
	p := filepath.Join(dir, name)
	c := fmt.Sprintf(plistTemplate, label, binPath, logOut, logErr)
	if err := os.WriteFile(p, []byte(c), 0644); err != nil {
		return "", err
	}
	return p, nil
}

// Bootstrap 注册并启动用户代理。
func Bootstrap(plistPath string) error {
	out, err := exec.Command("launchctl", "bootstrap", "gui/"+fmt.Sprint(os.Getuid()), plistPath).CombinedOutput()
	if err != nil {
		// 已加载则 kickstart 复活
		if _, err2 := exec.Command("launchctl", "kickstart", "-k", "gui/"+fmt.Sprint(os.Getuid())+"/"+filepath.Base(plistPath[:len(plistPath)-6])).CombinedOutput(); err2 == nil {
			return nil
		}
		return fmt.Errorf("bootstrap: %v: %s", err, out)
	}
	return nil
}

// Bootout 注销用户代理（不存在时静默）。
func Bootout(label string) error {
	uid := fmt.Sprint(os.Getuid())
	out, err := exec.Command("launchctl", "bootout", "gui/"+uid+"/"+label).CombinedOutput()
	if err != nil && len(out) == 0 {
		return nil // 已不在
	}
	return nil
}
