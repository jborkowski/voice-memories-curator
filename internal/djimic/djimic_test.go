package djimic

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jborkowski/vmc/internal/config"
)

const (
	testMedia  = "TEST MIC TX"
	testUUID   = "00000000-0000-0000-0000-000000000001"
	testVendor = 1234
	testProd   = 5678
)

type fakeSys struct {
	mu       sync.Mutex
	disks    map[string]string // diskutil target -> info output
	ioreg    string
	ejected  []string
	notes    []string
	launchd  [][]string
	printErr bool
}

func (f *fakeSys) DiskInfo(target string) (string, error) {
	if info, ok := f.disks[target]; ok {
		return info, nil
	}
	return "", errors.New("not a disk")
}

func (f *fakeSys) USBRegistry() (string, error) { return f.ioreg, nil }

func (f *fakeSys) EjectDisk(disk string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ejected = append(f.ejected, disk)
	return nil
}

func (f *fakeSys) Launchctl(args ...string) ([]byte, error) {
	f.launchd = append(f.launchd, args)
	if args[0] == "print" {
		if f.printErr {
			return nil, errors.New("not loaded")
		}
		return []byte("\tstate = running\n\tpath = /x.plist\n\tother = 1\n\truns = 3\n\tlast exit code = 0\n"), nil
	}
	return nil, nil
}

func (f *fakeSys) Notify(title, msg string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notes = append(f.notes, msg)
}
func (f *fakeSys) Sync() {}

func volInfo(parent, uuid string) string {
	return fmt.Sprintf("   Device Identifier:         %ss1\n   Part of Whole:             %s\n   Volume UUID:               %s\n", parent, parent, uuid)
}

func wholeInfo(media string) string {
	return fmt.Sprintf("   Device Identifier:         disk9\n   Device / Media Name:       %s\n", media)
}

func ioregWith(vendor, product int) string {
	return fmt.Sprintf("+-o Root  <class IORegistryEntry>\n  +-o Other@1  <class IOUSBHostDevice>\n    \"idVendor\" = 1\n    \"idProduct\" = 2\n  +-o Mic@2  <class IOUSBHostDevice>\n    \"idProduct\" = %d\n    \"idVendor\" = %d\n", product, vendor)
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

type env struct {
	tool    *Tool
	sys     *fakeSys
	volumes string
	vol     string
	root    string
	log     *bytes.Buffer
}

// newEnv mounts a fake transmitter at <volumes>/MIC with a recordings folder.
func newEnv(t *testing.T) *env {
	t.Helper()
	volumes := t.TempDir()
	vol := filepath.Join(volumes, "MIC")
	other := filepath.Join(volumes, "Other")
	os.MkdirAll(other, 0o755)
	writeFile(t, filepath.Join(vol, "DJI_Audio_001", "DJI_01_20260925_175801.WAV"), "aaaa")
	writeFile(t, filepath.Join(vol, "DJI_Audio_001", "sub", "DJI_02_20260925_180000.wav"), "bbbbbb")
	writeFile(t, filepath.Join(vol, "DJI_Audio_001", "._DJI_01_20260925_175801.WAV"), "appledouble")
	writeFile(t, filepath.Join(vol, "DJI_Audio_001", "readme.txt"), "not audio")
	writeFile(t, filepath.Join(vol, ".Trashes", "old.wav"), "trash")

	sys := &fakeSys{disks: map[string]string{
		vol:     volInfo("disk9", testUUID),
		"disk9": wholeInfo(testMedia),
		other:   volInfo("disk3", "00000000-0000-0000-0000-00000000FFFF"),
		"disk3": wholeInfo("Other Disk"),
	}}
	root := t.TempDir()
	log := &bytes.Buffer{}
	cfg := config.DefaultConfig().DJI
	cfg.Root = root
	cfg.MediaName = testMedia
	cfg.PullLock = filepath.Join(t.TempDir(), "pull.lock")
	cfg.MountWaitSecs = 2
	tool := &Tool{
		Cfg:        cfg,
		Sys:        sys,
		VolumesDir: volumes,
		Out:        log,
		Logf:       func(f string, a ...any) { fmt.Fprintf(log, f+"\n", a...) },
		Now:        func() time.Time { return time.Date(2026, 9, 25, 17, 58, 0, 0, time.Local) },
		Sleep:      func(time.Duration) {},
	}
	return &env{tool: tool, sys: sys, volumes: volumes, vol: vol, root: root, log: log}
}

func TestFindVolume(t *testing.T) {
	e := newEnv(t)

	if v, err := e.tool.FindVolume(); err != nil || v != e.vol {
		t.Fatalf("media match: %q, %v", v, err)
	}

	e.tool.Cfg.MediaName = ""
	e.tool.Cfg.VolumeUUID = strings.ToLower(testUUID)
	if v, err := e.tool.FindVolume(); err != nil || v != e.vol {
		t.Fatalf("uuid match: %q, %v", v, err)
	}

	e.tool.Cfg.VolumeUUID = ""
	e.tool.Cfg.USBVendor, e.tool.Cfg.USBProduct = testVendor, testProd
	if _, err := e.tool.FindVolume(); !errors.Is(err, ErrNotMounted) {
		t.Fatalf("usb ids without an attached device must not match, got %v", err)
	}
	e.sys.ioreg = ioregWith(testVendor, testProd)
	if v, err := e.tool.FindVolume(); err != nil || v != e.vol {
		t.Fatalf("usb+audio match: %q, %v", v, err)
	}

	os.Rename(filepath.Join(e.vol, "DJI_Audio_001"), filepath.Join(e.vol, "Music"))
	if _, err := e.tool.FindVolume(); !errors.Is(err, ErrNotMounted) {
		t.Fatalf("usb without audio folders must not match, got %v", err)
	}

	e.tool.Cfg.USBVendor, e.tool.Cfg.USBProduct = 0, 0
	if _, err := e.tool.FindVolume(); !errors.Is(err, ErrNotMounted) {
		t.Fatalf("unset fingerprints must never match, got %v", err)
	}
}

func TestUSBPresentRequiresBothIDsInOneDevice(t *testing.T) {
	e := newEnv(t)
	e.tool.Cfg.USBVendor, e.tool.Cfg.USBProduct = 1, testProd
	e.sys.ioreg = ioregWith(testVendor, testProd)
	if e.tool.USBPresent() {
		t.Fatal("vendor from one device and product from another must not match")
	}
}

func TestDetect(t *testing.T) {
	e := newEnv(t)
	if err := e.tool.Detect(); err != nil {
		t.Fatal(err)
	}
	out := e.log.String()
	for _, want := range []string{"mounted:     yes", "volume:      " + e.vol, "media name:  " + testMedia, "volume uuid: " + testUUID, "usb present: no", "DJI_Audio_001"} {
		if !strings.Contains(out, want) {
			t.Errorf("detect output missing %q:\n%s", want, out)
		}
	}

	e.log.Reset()
	e.tool.Cfg.MediaName = "absent"
	if err := e.tool.Detect(); !errors.Is(err, ErrNotMounted) {
		t.Fatalf("want ErrNotMounted, got %v", err)
	}
	if !strings.Contains(e.log.String(), "mounted:     no") {
		t.Errorf("absent output: %s", e.log.String())
	}
}

func TestPullCopiesDeletesAndEjects(t *testing.T) {
	e := newEnv(t)
	res, err := e.tool.Pull(DefaultPullOptions(e.tool.Cfg))
	if err != nil {
		t.Fatalf("Pull: %v\n%s", err, e.log)
	}
	dest := filepath.Join(e.root, "2026-09-25-17-58")
	if res.DestDir != dest || res.Files != 2 || res.Copied != 2 || res.Deleted != 2 || !res.Ejected {
		t.Fatalf("unexpected result %+v", res)
	}
	for rel, body := range map[string]string{
		"DJI_Audio_001/DJI_01_20260925_175801.WAV":     "aaaa",
		"DJI_Audio_001/sub/DJI_02_20260925_180000.wav": "bbbbbb",
	} {
		got, err := os.ReadFile(filepath.Join(dest, rel))
		if err != nil || string(got) != body {
			t.Errorf("%s = %q, %v", rel, got, err)
		}
		if _, err := os.Stat(filepath.Join(e.vol, rel)); !os.IsNotExist(err) {
			t.Errorf("%s should be deleted from the device", rel)
		}
	}
	if _, err := os.Stat(filepath.Join(dest, "DJI_Audio_001", "readme.txt")); !os.IsNotExist(err) {
		t.Error("non-audio files must not be copied")
	}
	if _, err := os.Stat(filepath.Join(dest, ".Trashes")); !os.IsNotExist(err) {
		t.Error(".Trashes must be skipped")
	}
	if len(e.sys.ejected) != 1 || e.sys.ejected[0] != "disk9" {
		t.Errorf("should eject the whole disk once, got %v", e.sys.ejected)
	}
	if len(e.sys.notes) == 0 || !strings.Contains(e.sys.notes[0], "Pulled 2 files into 2026-09-25-17-58") {
		t.Errorf("notifications: %v", e.sys.notes)
	}
	if _, err := os.Stat(e.tool.Cfg.PullLock); !os.IsNotExist(err) {
		t.Error("pull lock must be released")
	}
}

func TestPullRemovesEmptiedAudioDirs(t *testing.T) {
	e := newEnv(t)
	writeFile(t, filepath.Join(e.vol, "DJI_Audio_002", "DJI_03_20260925_190000.WAV"), "cc")
	if _, err := e.tool.Pull(DefaultPullOptions(e.tool.Cfg)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.vol, "DJI_Audio_002")); !os.IsNotExist(err) {
		t.Error("emptied audio dir should be removed")
	}
	if _, err := os.Stat(filepath.Join(e.vol, "DJI_Audio_001")); err != nil {
		t.Error("audio dir with leftover non-audio files must stay")
	}
}

func TestPullKeepNoEject(t *testing.T) {
	e := newEnv(t)
	opts := PullOptions{Stamp: "custom", Dest: t.TempDir()}
	res, err := e.tool.Pull(opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Copied != 2 || res.Deleted != 0 || res.Ejected || len(e.sys.ejected) != 0 {
		t.Fatalf("keep/no-eject result %+v, ejected %v", res, e.sys.ejected)
	}
	if res.DestDir != filepath.Join(opts.Dest, "custom") {
		t.Errorf("dest dir = %s", res.DestDir)
	}
	if _, err := os.Stat(filepath.Join(e.vol, "DJI_Audio_001", "DJI_01_20260925_175801.WAV")); err != nil {
		t.Error("--keep must leave files on the device")
	}
}

func TestPullDryRun(t *testing.T) {
	e := newEnv(t)
	opts := DefaultPullOptions(e.tool.Cfg)
	opts.DryRun = true
	res, err := e.tool.Pull(opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Copied != 0 || len(e.sys.ejected) != 0 {
		t.Fatalf("dry run changed state: %+v", res)
	}
	if entries, _ := os.ReadDir(e.root); len(entries) != 0 {
		t.Error("dry run must not create the destination")
	}
	if !strings.Contains(e.log.String(), "would copy  DJI_Audio_001/DJI_01_20260925_175801.WAV") ||
		!strings.Contains(e.log.String(), "would eject") {
		t.Errorf("dry run log:\n%s", e.log)
	}
}

func TestPullAbsent(t *testing.T) {
	e := newEnv(t)
	e.tool.Cfg.MediaName = "absent"

	if _, err := e.tool.Pull(DefaultPullOptions(e.tool.Cfg)); !errors.Is(err, ErrNotMounted) {
		t.Fatalf("want ErrNotMounted, got %v", err)
	}

	opts := DefaultPullOptions(e.tool.Cfg)
	opts.IfPresent = true
	if _, err := e.tool.Pull(opts); err != nil {
		t.Fatalf("--if-present must succeed when absent: %v", err)
	}

	e.tool.Cfg.USBVendor, e.tool.Cfg.USBProduct = testVendor, testProd
	e.sys.ioreg = ioregWith(testVendor, testProd)
	os.Rename(filepath.Join(e.vol, "DJI_Audio_001"), filepath.Join(e.vol, "Music"))
	sleeps := 0
	e.tool.Sleep = func(time.Duration) { sleeps++ }
	if _, err := e.tool.Pull(opts); err != nil {
		t.Fatal(err)
	}
	if sleeps != e.tool.Cfg.MountWaitSecs {
		t.Errorf("should wait mount_wait_secs while USB is attached, slept %d", sleeps)
	}
}

func TestPullSourceOverrideEmptyCardStaysMounted(t *testing.T) {
	e := newEnv(t)
	empty := filepath.Join(e.volumes, "EMPTY")
	os.MkdirAll(filepath.Join(empty, "DJI_Audio_001"), 0o755)
	opts := DefaultPullOptions(e.tool.Cfg)
	opts.Source = empty
	res, err := e.tool.Pull(opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Copied != 0 || len(e.sys.ejected) != 0 {
		t.Fatalf("empty card must not eject: %+v %v", res, e.sys.ejected)
	}

	os.RemoveAll(filepath.Join(empty, "DJI_Audio_001"))
	if _, err := e.tool.Pull(opts); err != nil || len(e.sys.ejected) != 0 {
		t.Fatalf("no audio folders: err %v ejected %v", err, e.sys.ejected)
	}
}

func TestPullLock(t *testing.T) {
	e := newEnv(t)
	lock := e.tool.Cfg.PullLock
	os.MkdirAll(lock, 0o755)
	writeFile(t, filepath.Join(lock, "pid"), fmt.Sprintf("%d\n", os.Getpid()))
	res, err := e.tool.Pull(DefaultPullOptions(e.tool.Cfg))
	if err != nil || res.Copied != 0 {
		t.Fatalf("held lock should skip: %+v %v", res, err)
	}
	if !strings.Contains(e.log.String(), "already running") {
		t.Errorf("log:\n%s", e.log)
	}

	// A pid that cannot exist marks the lock stale.
	writeFile(t, filepath.Join(lock, "pid"), "99999999\n")
	res, err = e.tool.Pull(DefaultPullOptions(e.tool.Cfg))
	if err != nil || res.Copied != 2 {
		t.Fatalf("stale lock should be replaced: %+v %v", res, err)
	}
}

func TestHumanBytes(t *testing.T) {
	for n, want := range map[int64]string{0: "0B", 1023: "1023B", 1536: "1.5KB", 5 << 20: "5.0MB"} {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %s, want %s", n, got, want)
		}
	}
}

func TestWatch(t *testing.T) {
	sys := &fakeSys{}
	dir := t.TempDir()
	w := &Watch{
		Sys:       sys,
		Binary:    "/opt/test/bin/vmc",
		PlistPath: filepath.Join(dir, "LaunchAgents", AgentLabel+".plist"),
		LogFile:   filepath.Join(dir, "logs", "a&b.log"),
		Home:      "/home/test",
		UID:       501,
	}
	if err := w.Enable(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(w.PlistPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"<string>/opt/test/bin/vmc</string>\n    <string>dji</string>\n    <string>pull</string>\n    <string>--if-present</string>\n    <string>--quiet</string>",
		"<string>/Volumes</string>",
		"a&amp;b.log",
		"<string>" + AgentLabel + "</string>",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("plist missing %q:\n%s", want, body)
		}
	}
	want := [][]string{{"bootout", "gui/501/" + AgentLabel}, {"bootstrap", "gui/501", w.PlistPath}}
	if fmt.Sprint(sys.launchd) != fmt.Sprint(want) {
		t.Errorf("launchctl calls = %v", sys.launchd)
	}

	loaded, lines := w.Status()
	if !loaded || len(lines) != 4 {
		t.Errorf("status = %v %q", loaded, lines)
	}
	sys.printErr = true
	if loaded, _ := w.Status(); loaded {
		t.Error("status should report disabled")
	}
}

func TestStableBinary(t *testing.T) {
	if got := stableBinary("/usr/local/bin/vmc"); got != "/usr/local/bin/vmc" {
		t.Errorf("non-cellar path changed: %s", got)
	}
	prefix := t.TempDir()
	opt := filepath.Join(prefix, "opt", "vmc", "bin", "vmc")
	writeFile(t, opt, "")
	if got := stableBinary(filepath.Join(prefix, "Cellar", "vmc", "1.2.3", "bin", "vmc")); got != opt {
		t.Errorf("cellar path = %s, want %s", got, opt)
	}
}

func TestPreferServiceBinary(t *testing.T) {
	home := t.TempDir()
	plain := filepath.Join(t.TempDir(), "vmc")
	writeExec(t, plain, "")

	if got := preferServiceBinary(home, plain); got != plain {
		t.Errorf("no FDA paths: got %s want %s", got, plain)
	}

	svcDir := filepath.Join(t.TempDir(), "bin")
	vmc := filepath.Join(svcDir, "vmc")
	svc := filepath.Join(svcDir, "vmc-service")
	writeExec(t, vmc, "")
	writeExec(t, svc, "")
	if got := preferServiceBinary(home, vmc); got != svc {
		t.Errorf("vmc-service: got %s want %s", got, svc)
	}
}

func writeExec(t *testing.T, path, body string) {
	t.Helper()
	writeFile(t, path, body)
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestFileLogger(t *testing.T) {
	var out bytes.Buffer
	path := filepath.Join(t.TempDir(), "sub", "dji.log")
	l := &FileLogger{Path: path, Stdout: &out, Now: func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }}
	l.Logf("hello %d", 1)
	l.Quiet = true
	l.Logf("quiet")
	b, _ := os.ReadFile(path)
	if string(b) != "2026-01-02 03:04:05 hello 1\n2026-01-02 03:04:05 quiet\n" || out.String() != "hello 1\n" {
		t.Errorf("file %q stdout %q", b, out.String())
	}
}
