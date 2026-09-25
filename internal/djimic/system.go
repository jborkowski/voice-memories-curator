package djimic

import (
	"fmt"
	"os/exec"
	"strings"
)

// System is the macOS surface djimic shells out to; tests swap in a fake.
type System interface {
	// DiskInfo returns `diskutil info <target>` output.
	DiskInfo(target string) (string, error)
	// USBRegistry returns `ioreg -p IOUSB -l -w 0` output.
	USBRegistry() (string, error)
	// EjectDisk force-unmounts and ejects a whole disk (e.g. disk4).
	EjectDisk(disk string) error
	// Launchctl runs launchctl with args and returns combined output.
	Launchctl(args ...string) ([]byte, error)
	// Notify posts a user notification; failures are ignored.
	Notify(title, msg string)
	// Sync flushes filesystem buffers.
	Sync()
}

// ExecSystem implements System with diskutil, ioreg, launchctl and osascript.
type ExecSystem struct{}

func (ExecSystem) DiskInfo(target string) (string, error) {
	out, err := exec.Command("diskutil", "info", target).Output()
	return string(out), err
}

func (ExecSystem) USBRegistry() (string, error) {
	out, err := exec.Command("ioreg", "-p", "IOUSB", "-l", "-w", "0").Output()
	return string(out), err
}

func (ExecSystem) EjectDisk(disk string) error {
	_ = exec.Command("diskutil", "unmountDisk", "force", disk).Run()
	out, err := exec.Command("diskutil", "eject", disk).CombinedOutput()
	if err != nil {
		return fmt.Errorf("diskutil eject %s: %w: %s", disk, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (ExecSystem) Launchctl(args ...string) ([]byte, error) {
	return exec.Command("launchctl", args...).CombinedOutput()
}

func (ExecSystem) Notify(title, msg string) {
	if _, err := exec.LookPath("osascript"); err != nil {
		return
	}
	script := fmt.Sprintf("display notification %s with title %s", appleScriptString(msg), appleScriptString(title))
	_ = exec.Command("osascript", "-e", script).Run()
}

func (ExecSystem) Sync() {
	_ = exec.Command("sync").Run()
}

func appleScriptString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
