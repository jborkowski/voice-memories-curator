package djimic

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
)

// AgentLabel is the launchd label of the /Volumes watcher.
const AgentLabel = "com.jborkowski.vmc.dji-watch"

// Watch describes the launchd agent that runs `vmc dji pull --if-present
// --quiet` whenever /Volumes changes.
type Watch struct {
	Sys       System
	Binary    string // vmc executable
	PlistPath string
	LogFile   string
	Home      string
	UID       int
}

// NewWatch returns a Watch for the current user and executable.
func NewWatch(logFile string) (*Watch, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	bin, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return &Watch{
		Sys:       ExecSystem{},
		Binary:    preferServiceBinary(home, bin),
		PlistPath: filepath.Join(home, "Library", "LaunchAgents", AgentLabel+".plist"),
		LogFile:   logFile,
		Home:      home,
		UID:       os.Getuid(),
	}, nil
}

var cellarRe = regexp.MustCompile(`^(.*)/Cellar/vmc/[^/]+/bin/vmc$`)

// preferServiceBinary mirrors brew services: prefer the FDA-granted VMC.app
// binary, else vmc-service (which re-resolves to that app), else the stable
// Homebrew opt path. Plain Cellar/opt binaries often lack Full Disk Access,
// so /Volumes and iCloud destinations fail silently under launchd.
func preferServiceBinary(home, bin string) string {
	for _, rel := range []string{
		"Applications/VMC.app/Contents/Resources/vmc",
		"Desktop/VMC.app/Contents/Resources/vmc",
	} {
		p := filepath.Join(home, rel)
		if isExecutable(p) {
			return p
		}
	}
	stable := stableBinary(bin)
	for _, dir := range []string{filepath.Dir(stable), filepath.Dir(bin)} {
		svc := filepath.Join(dir, "vmc-service")
		if isExecutable(svc) {
			return svc
		}
	}
	return stable
}

func isExecutable(path string) bool {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return false
	}
	return fi.Mode()&0o111 != 0
}

// stableBinary maps a versioned Homebrew Cellar path to the opt symlink so
// the agent survives `brew upgrade`.
func stableBinary(bin string) string {
	m := cellarRe.FindStringSubmatch(bin)
	if m == nil {
		return bin
	}
	opt := m[1] + "/opt/vmc/bin/vmc"
	if _, err := os.Stat(opt); err == nil {
		return opt
	}
	return bin
}

func (w *Watch) target() string {
	return fmt.Sprintf("gui/%d/%s", w.UID, AgentLabel)
}

func (w *Watch) domain() string {
	return fmt.Sprintf("gui/%d", w.UID)
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

var plistTmpl = template.Must(template.New("plist").Funcs(template.FuncMap{"x": xmlEscape}).Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>{{x .Label}}</string>
  <key>ProgramArguments</key>
  <array>
    <string>{{x .Binary}}</string>
    <string>dji</string>
    <string>pull</string>
    <string>--if-present</string>
    <string>--quiet</string>
  </array>
  <key>WatchPaths</key>
  <array>
    <string>/Volumes</string>
  </array>
  <key>ThrottleInterval</key>
  <integer>3</integer>
  <key>RunAtLoad</key>
  <false/>
  <key>Nice</key>
  <integer>5</integer>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
    <key>HOME</key>
    <string>{{x .Home}}</string>
  </dict>
  <key>StandardOutPath</key>
  <string>{{x .LogFile}}</string>
  <key>StandardErrorPath</key>
  <string>{{x .LogFile}}</string>
</dict>
</plist>
`))

// Plist renders the launchd agent definition.
func (w *Watch) Plist() (string, error) {
	var b bytes.Buffer
	err := plistTmpl.Execute(&b, map[string]string{
		"Label": AgentLabel, "Binary": w.Binary, "Home": w.Home, "LogFile": w.LogFile,
	})
	return b.String(), err
}

// Enable writes the plist and (re)bootstraps the agent.
func (w *Watch) Enable() error {
	body, err := w.Plist()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(w.PlistPath), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(w.LogFile), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(w.PlistPath, []byte(body), 0o644); err != nil {
		return err
	}
	_, _ = w.Sys.Launchctl("bootout", w.target())
	if out, err := w.Sys.Launchctl("bootstrap", w.domain(), w.PlistPath); err != nil {
		return fmt.Errorf("launchctl bootstrap: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Disable unloads the agent; the plist is left in place.
func (w *Watch) Disable() {
	_, _ = w.Sys.Launchctl("bootout", w.target())
}

// Status reports whether the agent is loaded plus selected launchctl lines.
func (w *Watch) Status() (bool, []string) {
	out, err := w.Sys.Launchctl("print", w.target())
	if err != nil {
		return false, nil
	}
	var lines []string
	for _, line := range strings.Split(string(out), "\n") {
		for _, key := range []string{"state =", "path =", "runs =", "last exit"} {
			if strings.Contains(line, key) {
				lines = append(lines, line)
				break
			}
		}
	}
	return true, lines
}
