package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jborkowski/vmc/internal/djimic"
)

const testMedia = "TEST MIC"

type fakeDJISystem struct {
	disks     map[string]string
	ejected   []string
	ejectErr  error
	launchctl [][]string
	loaded    bool
}

func (f *fakeDJISystem) DiskInfo(target string) (string, error) {
	if info, ok := f.disks[target]; ok {
		return info, nil
	}
	return "", errors.New("no such disk")
}
func (f *fakeDJISystem) USBRegistry() (string, error) { return "", nil }
func (f *fakeDJISystem) EjectDisk(disk string) error {
	if f.ejectErr != nil {
		return f.ejectErr
	}
	f.ejected = append(f.ejected, disk)
	return nil
}
func (f *fakeDJISystem) Launchctl(args ...string) ([]byte, error) {
	f.launchctl = append(f.launchctl, args)
	if args[0] == "print" && !f.loaded {
		return nil, errors.New("not loaded")
	}
	return []byte("\tstate = running\n\tother = x\n"), nil
}
func (f *fakeDJISystem) Notify(string, string) {}
func (f *fakeDJISystem) Sync()                 {}

type djiEnv struct {
	t       *testing.T
	home    string
	volumes string
	vol     string
	dest    string
	logFile string
	sys     *fakeDJISystem
}

// newDJIEnv isolates HOME/config and swaps the system for a fake. The mic
// volume is created (and matched by media name) only when mounted is true.
func newDJIEnv(t *testing.T, mounted bool) *djiEnv {
	t.Helper()
	tmp := t.TempDir()
	e := &djiEnv{
		t:       t,
		home:    filepath.Join(tmp, "home"),
		volumes: filepath.Join(tmp, "Volumes"),
		dest:    filepath.Join(tmp, "inbox"),
		logFile: filepath.Join(tmp, "dji.log"),
		sys:     &fakeDJISystem{disks: map[string]string{}},
	}
	e.vol = filepath.Join(e.volumes, "MIC")
	mustMkdir(t, filepath.Join(e.home, ".config", "vmc"))
	mustMkdir(t, e.volumes)
	conf := fmt.Sprintf(`[dji]
media_name = %q
notify = false
mount_wait_secs = 0
pull_lock = %q
log_file = %q
`, testMedia, filepath.Join(tmp, "pull.lock"), e.logFile)
	mustWrite(t, filepath.Join(e.home, ".config", "vmc", "config.toml"), conf)
	t.Setenv("HOME", e.home)
	t.Setenv("HF_TOKEN", "")

	if mounted {
		mustMkdir(t, e.vol)
		e.sys.disks[e.vol] = "   Part of Whole:            disk9\n   Volume UUID:              TEST-UUID\n"
		e.sys.disks["disk9"] = "   Device / Media Name:      " + testMedia + "\n"
	}

	oldSys, oldVols := djiSystem, djiVolumesDir
	djiSystem = func() djimic.System { return e.sys }
	djiVolumesDir = e.volumes
	t.Cleanup(func() { djiSystem, djiVolumesDir = oldSys, oldVols })
	return e
}

func (e *djiEnv) addRecording(rel, body string) string {
	p := filepath.Join(e.vol, rel)
	mustMkdir(e.t, filepath.Dir(p))
	mustWrite(e.t, p, body)
	return p
}

func resetDJIFlags() {
	djiDeleteAfter, djiEjectAfter = nil, nil
	djiDryRun, djiIfPresent, djiQuiet = false, false, false
	djiSource, djiDest, djiStamp = "", "", ""
}

func runVMC(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	resetDJIFlags()
	var out, errb bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errb)
	rootCmd.SetArgs(args)
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	})
	err = rootCmd.Execute()
	return out.String(), errb.String(), err
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, p, body string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func TestDJIPullDefaultsCopyDeleteEject(t *testing.T) {
	e := newDJIEnv(t, true)
	src := e.addRecording("DJI_Audio_001/DJI_01_20260101_120000.WAV", "wav-bytes")

	out, _, err := runVMC(t, "dji", "pull", "--dest", e.dest, "--stamp", "S")
	if err != nil {
		t.Fatalf("pull: %v\n%s", err, out)
	}
	got, err := os.ReadFile(filepath.Join(e.dest, "S", "DJI_Audio_001", "DJI_01_20260101_120000.WAV"))
	if err != nil || string(got) != "wav-bytes" {
		t.Fatalf("copied file = %q, %v", got, err)
	}
	if exists(src) || exists(filepath.Dir(src)) {
		t.Errorf("source file or empty audio dir left on device")
	}
	if len(e.sys.ejected) != 1 || e.sys.ejected[0] != "disk9" {
		t.Errorf("ejected = %v, want [disk9]", e.sys.ejected)
	}
	if !strings.Contains(out, "done. copied 1 / 1 files") {
		t.Errorf("stdout missing summary:\n%s", out)
	}
}

func TestDJIPullKeepNoEject(t *testing.T) {
	e := newDJIEnv(t, true)
	src := e.addRecording("DJI_Audio_001/a.wav", "x")

	if _, _, err := runVMC(t, "dji", "pull", "--keep", "--no-eject", "--dest", e.dest, "--stamp", "S"); err != nil {
		t.Fatal(err)
	}
	if !exists(src) {
		t.Error("--keep deleted the source")
	}
	if !exists(filepath.Join(e.dest, "S", "DJI_Audio_001", "a.wav")) {
		t.Error("file not copied")
	}
	if len(e.sys.ejected) != 0 {
		t.Errorf("--no-eject ejected %v", e.sys.ejected)
	}
}

func TestDJIPullOpposingFlagsLastWins(t *testing.T) {
	e := newDJIEnv(t, true)
	src := e.addRecording("DJI_Audio_001/a.wav", "x")

	if _, _, err := runVMC(t, "dji", "pull", "--keep", "--delete", "--no-eject", "--eject", "--dest", e.dest, "--stamp", "S"); err != nil {
		t.Fatal(err)
	}
	if exists(src) {
		t.Error("--keep --delete should delete (last wins)")
	}
	if len(e.sys.ejected) != 1 {
		t.Errorf("--no-eject --eject should eject (last wins), ejected %v", e.sys.ejected)
	}

	e2 := newDJIEnv(t, true)
	src2 := e2.addRecording("DJI_Audio_001/a.wav", "x")
	if _, _, err := runVMC(t, "dji", "pull", "--delete", "--keep", "--eject", "--no-eject", "--dest", e2.dest, "--stamp", "S"); err != nil {
		t.Fatal(err)
	}
	if !exists(src2) || len(e2.sys.ejected) != 0 {
		t.Errorf("--delete --keep --eject --no-eject: src kept=%v ejected=%v", exists(src2), e2.sys.ejected)
	}
}

func TestDJIPullDryRunTouchesNothing(t *testing.T) {
	e := newDJIEnv(t, true)
	src := e.addRecording("DJI_Audio_001/a.wav", "x")

	out, _, err := runVMC(t, "dji", "pull", "--dry-run", "--dest", e.dest, "--stamp", "S")
	if err != nil {
		t.Fatal(err)
	}
	if !exists(src) || exists(e.dest) || len(e.sys.ejected) != 0 {
		t.Errorf("dry-run side effects: src=%v dest=%v ejected=%v", exists(src), exists(e.dest), e.sys.ejected)
	}
	for _, want := range []string{"would copy  DJI_Audio_001/a.wav", "would delete DJI_Audio_001/a.wav from device", "would eject"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q:\n%s", want, out)
		}
	}
}

func TestDJIPullEmptyCardLeavesMounted(t *testing.T) {
	e := newDJIEnv(t, true)

	out, _, err := runVMC(t, "dji", "pull", "--dest", e.dest)
	if err != nil {
		t.Fatalf("empty card should exit 0: %v", err)
	}
	if !strings.Contains(out, "empty card; leaving mounted") || len(e.sys.ejected) != 0 {
		t.Errorf("ejected=%v out:\n%s", e.sys.ejected, out)
	}

	mustMkdir(t, filepath.Join(e.vol, "DJI_Audio_001"))
	out, _, err = runVMC(t, "dji", "pull", "--dest", e.dest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no audio files found on the device (leaving mounted)") || len(e.sys.ejected) != 0 {
		t.Errorf("ejected=%v out:\n%s", e.sys.ejected, out)
	}
}

func TestDJIPullFailedCopyNoEjectNoDelete(t *testing.T) {
	e := newDJIEnv(t, true)
	src := e.addRecording("DJI_Audio_001/a.wav", "x")
	blocked := filepath.Join(e.dest, "S", "DJI_Audio_001")
	mustMkdir(t, blocked)
	if err := os.Chmod(blocked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(blocked, 0o755) })

	_, stderr, err := runVMC(t, "dji", "pull", "--dest", e.dest, "--stamp", "S")
	if !IsReported(err) {
		t.Fatalf("err = %v, want reported failure", err)
	}
	if !strings.HasPrefix(stderr, "error: ") {
		t.Errorf("stderr = %q", stderr)
	}
	if !exists(src) || len(e.sys.ejected) != 0 {
		t.Errorf("failed copy: src kept=%v ejected=%v", exists(src), e.sys.ejected)
	}
}

func TestDJIPullIfPresent(t *testing.T) {
	newDJIEnv(t, false)

	out, _, err := runVMC(t, "dji", "pull", "--if-present")
	if err != nil {
		t.Fatalf("--if-present should exit 0: %v", err)
	}
	if !strings.Contains(out, "no DJI Mic mounted; nothing to do") {
		t.Errorf("stdout:\n%s", out)
	}

	_, stderr, err := runVMC(t, "dji", "pull")
	if !IsReported(err) || !strings.HasPrefix(stderr, "error: DJI Mic is not mounted") {
		t.Errorf("err=%v stderr=%q", err, stderr)
	}
}

func TestDJIPullQuietStillLogs(t *testing.T) {
	e := newDJIEnv(t, true)
	e.addRecording("DJI_Audio_001/a.wav", "x")

	out, _, err := runVMC(t, "dji", "pull", "--quiet", "--source", e.vol, "--dest", e.dest, "--stamp", "S")
	if err != nil {
		t.Fatal(err)
	}
	if out != "" {
		t.Errorf("--quiet stdout = %q", out)
	}
	logged, _ := os.ReadFile(e.logFile)
	if !strings.Contains(string(logged), "done. copied 1 / 1 files") {
		t.Errorf("log file missing summary:\n%s", logged)
	}
}

func TestDJIDetect(t *testing.T) {
	newDJIEnv(t, false)
	out, _, err := runVMC(t, "dji", "detect")
	if !IsReported(err) || !strings.Contains(out, "mounted:     no") {
		t.Errorf("absent: err=%v out:\n%s", err, out)
	}

	e := newDJIEnv(t, true)
	out, _, err = runVMC(t, "dji", "detect")
	if err != nil || !strings.Contains(out, "volume:      "+e.vol) || !strings.Contains(out, "media name:  "+testMedia) {
		t.Errorf("present: err=%v out:\n%s", err, out)
	}
}

func TestDJIEject(t *testing.T) {
	newDJIEnv(t, false)
	_, stderr, err := runVMC(t, "dji", "eject")
	if !IsReported(err) || stderr != "error: DJI Mic is not mounted\n" {
		t.Errorf("absent: err=%v stderr=%q", err, stderr)
	}

	e := newDJIEnv(t, true)
	if _, _, err := runVMC(t, "dji", "eject"); err != nil || len(e.sys.ejected) != 1 {
		t.Errorf("present: err=%v ejected=%v", err, e.sys.ejected)
	}
}

func TestDJIWatch(t *testing.T) {
	e := newDJIEnv(t, false)

	_, stderr, err := runVMC(t, "dji", "watch")
	if !IsReported(err) || stderr != "error: watch needs enable, disable, or status\n" {
		t.Errorf("no action: err=%v stderr=%q", err, stderr)
	}

	out, _, err := runVMC(t, "dji", "watch", "enable")
	if err != nil {
		t.Fatal(err)
	}
	plist := filepath.Join(e.home, "Library", "LaunchAgents", djimic.AgentLabel+".plist")
	body, err := os.ReadFile(plist)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<string>pull</string>", "<string>--if-present</string>", "<string>--quiet</string>", "<string>/Volumes</string>"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("plist missing %s", want)
		}
	}
	if !strings.Contains(out, "Watcher is armed") {
		t.Errorf("enable stdout:\n%s", out)
	}
	last := e.sys.launchctl[len(e.sys.launchctl)-1]
	if last[0] != "bootstrap" || last[2] != plist {
		t.Errorf("last launchctl = %v", last)
	}

	out, _, _ = runVMC(t, "dji", "watch", "status")
	if !strings.HasPrefix(out, "watch: disabled") {
		t.Errorf("status (unloaded):\n%s", out)
	}
	e.sys.loaded = true
	out, _, _ = runVMC(t, "dji", "watch", "status")
	if !strings.HasPrefix(out, "watch: enabled\n\tstate = running\nplist: ") {
		t.Errorf("status (loaded):\n%s", out)
	}

	if _, _, err := runVMC(t, "dji", "watch", "disable"); err != nil {
		t.Fatal(err)
	}
	if last := e.sys.launchctl[len(e.sys.launchctl)-1]; last[0] != "bootout" {
		t.Errorf("disable launchctl = %v", last)
	}
}

func TestDJIUsageErrors(t *testing.T) {
	newDJIEnv(t, false)

	_, stderr, err := runVMC(t, "dji", "pull", "--bogus")
	if !IsReported(err) || !strings.HasPrefix(stderr, "error: unknown flag: --bogus") {
		t.Errorf("unknown flag: err=%v stderr=%q", err, stderr)
	}
	_, stderr, err = runVMC(t, "dji", "nope")
	if !IsReported(err) || stderr != "error: unknown command: nope\n" {
		t.Errorf("unknown command: err=%v stderr=%q", err, stderr)
	}
	if _, _, err = runVMC(t, "dji"); !IsReported(err) {
		t.Errorf("bare dji should exit non-zero, err=%v", err)
	}
}
