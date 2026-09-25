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
	otherUUID     = "00000000-0000-0000-0000-0000000000AA"
	partitionUUID = "00000000-0000-0000-0000-0000000000BB"
)

// diskutilVolume mimics `diskutil info <volume>` for a FAT32 partition.
func diskutilVolume(ident, parent, name, uuid string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "   Device Identifier:         %s\n", ident)
	fmt.Fprintf(&b, "   Device Node:               /dev/%s\n", ident)
	b.WriteString("   Whole:                     No\n")
	if parent != "" {
		fmt.Fprintf(&b, "   Part of Whole:             %s\n", parent)
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "   Volume Name:               %s\n", name)
	b.WriteString("   Mounted:                   Yes\n")
	fmt.Fprintf(&b, "   Mount Point:               /Volumes/%s\n", name)
	b.WriteString("\n")
	b.WriteString("   Partition Type:            DOS_FAT_32\n")
	b.WriteString("   File System Personality:   MS-DOS FAT32\n")
	if uuid != "" {
		fmt.Fprintf(&b, "   Volume UUID:               %s\n", uuid)
	}
	fmt.Fprintf(&b, "   Disk / Partition UUID:     %s\n", partitionUUID)
	return b.String()
}

// diskutilWhole mimics `diskutil info <disk>` for the parent USB disk.
func diskutilWhole(ident, media string) string {
	return fmt.Sprintf("   Device Identifier:         %s\n"+
		"   Device Node:               /dev/%s\n"+
		"   Whole:                     Yes\n"+
		"   Part of Whole:             %s\n"+
		"   Device / Media Name:       %s\n"+
		"\n"+
		"   Protocol:                  USB\n"+
		"   Removable Media:           Removable\n", ident, ident, ident, media)
}

func TestFindVolumeTable(t *testing.T) {
	type fingerprints struct {
		media, uuid     string
		vendor, product int
	}
	type setup struct {
		// volume name -> diskutil info; "" means DiskInfo errors for it.
		volInfo map[string]string
		whole   map[string]string
		audio   []string // volumes that carry a DJI_Audio_* folder
		ioreg   string
	}
	std := setup{
		volInfo: map[string]string{
			"MIC":       diskutilVolume("disk9s1", "disk9", "MIC", testUUID),
			"Other Vol": diskutilVolume("disk3s1", "disk3", "Other Vol", otherUUID),
		},
		whole: map[string]string{
			"disk9": diskutilWhole("disk9", testMedia),
			"disk3": diskutilWhole("disk3", "Generic Flash"),
		},
		audio: []string{"MIC"},
	}

	cases := []struct {
		name string
		fp   fingerprints
		s    setup
		want string // volume name, "" for ErrNotMounted
	}{
		{"media exact", fingerprints{media: testMedia}, std, "MIC"},
		{"media other disk", fingerprints{media: "Generic Flash"}, std, "Other Vol"},
		{"media prefix is not a match", fingerprints{media: "TEST MIC"}, std, ""},
		{"media case sensitive", fingerprints{media: strings.ToLower(testMedia)}, std, ""},
		{"media wins over uuid", fingerprints{media: testMedia, uuid: otherUUID}, std, "MIC"},
		{"media miss falls back to uuid", fingerprints{media: "absent", uuid: otherUUID}, std, "Other Vol"},
		{"uuid case insensitive", fingerprints{uuid: strings.ToLower(testUUID)}, std, "MIC"},
		{"partition uuid is not volume uuid", fingerprints{uuid: partitionUUID}, std, ""},
		{"no fingerprints", fingerprints{}, std, ""},
		{"usb ids without device attached", fingerprints{vendor: testVendor, product: testProd}, std, ""},
		{"usb ids with device picks audio volume", fingerprints{vendor: testVendor, product: testProd},
			setup{volInfo: std.volInfo, whole: std.whole, audio: []string{"Other Vol"}, ioreg: ioregWith(testVendor, testProd)}, "Other Vol"},
		{"usb ids wrong product", fingerprints{vendor: testVendor, product: testProd + 1},
			setup{volInfo: std.volInfo, whole: std.whole, audio: std.audio, ioreg: ioregWith(testVendor, testProd)}, ""},
		{"usb ids half unset", fingerprints{vendor: testVendor},
			setup{volInfo: std.volInfo, whole: std.whole, audio: std.audio, ioreg: ioregWith(testVendor, testProd)}, ""},
		{"diskutil errors everywhere", fingerprints{media: testMedia, uuid: testUUID},
			setup{volInfo: map[string]string{"MIC": "", "Other Vol": ""}, audio: std.audio}, ""},
		{"no Part of Whole: media unresolvable, uuid still works", fingerprints{media: testMedia, uuid: testUUID},
			setup{volInfo: map[string]string{"MIC": diskutilVolume("disk9s1", "", "MIC", testUUID)}, whole: std.whole}, "MIC"},
		{"no Part of Whole and no uuid line", fingerprints{media: testMedia, uuid: testUUID},
			setup{volInfo: map[string]string{"MIC": diskutilVolume("disk9s1", "", "MIC", "")}, whole: std.whole}, ""},
		{"whole disk info missing", fingerprints{media: testMedia},
			setup{volInfo: std.volInfo, whole: map[string]string{}}, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			volumes := t.TempDir()
			// A plain file in /Volumes must never be treated as a volume.
			writeFile(t, filepath.Join(volumes, "stray-file"), "x")
			sys := &fakeSys{disks: map[string]string{}, ioreg: tc.s.ioreg}
			for name, info := range tc.s.volInfo {
				p := filepath.Join(volumes, name)
				if err := os.MkdirAll(p, 0o755); err != nil {
					t.Fatal(err)
				}
				if info != "" {
					sys.disks[p] = info
				}
			}
			for disk, info := range tc.s.whole {
				sys.disks[disk] = info
			}
			for _, name := range tc.s.audio {
				if err := os.MkdirAll(filepath.Join(volumes, name, "DJI_Audio_001"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			cfg := config.DefaultConfig().DJI
			cfg.MediaName, cfg.VolumeUUID = tc.fp.media, tc.fp.uuid
			cfg.USBVendor, cfg.USBProduct = tc.fp.vendor, tc.fp.product
			tool := &Tool{Cfg: cfg, Sys: sys, VolumesDir: volumes}

			got, err := tool.FindVolume()
			if tc.want == "" {
				if !errors.Is(err, ErrNotMounted) {
					t.Fatalf("want ErrNotMounted, got %q, %v", got, err)
				}
				return
			}
			if err != nil || got != filepath.Join(volumes, tc.want) {
				t.Fatalf("got %q, %v; want %s", got, err, tc.want)
			}
		})
	}
}

func TestDiskField(t *testing.T) {
	info := diskutilVolume("disk9s1", "disk9", "MIC", testUUID)
	for key, want := range map[string]string{
		"Part of Whole": "disk9",
		"Volume UUID":   testUUID,
		"Mount Point":   "/Volumes/MIC",
		"UUID":          "",
		"Volume":        "",
		"Missing Key":   "",
	} {
		if got := diskField(info, key); got != want {
			t.Errorf("diskField(%q) = %q, want %q", key, got, want)
		}
	}
}

func assertOnDevice(t *testing.T, e *env, rel string, want bool) {
	t.Helper()
	_, err := os.Stat(filepath.Join(e.vol, rel))
	if want && err != nil {
		t.Errorf("%s should still be on the device: %v", rel, err)
	}
	if !want && !os.IsNotExist(err) {
		t.Errorf("%s should be gone from the device", rel)
	}
}

const (
	relA = "DJI_Audio_001/DJI_01_20260925_175801.WAV"
	relB = "DJI_Audio_001/sub/DJI_02_20260925_180000.wav"
)

func TestPullPartialCopyFailureKeepsSourceAndSkipsEject(t *testing.T) {
	e := newEnv(t)
	// A non-empty directory at the destination makes the rename for relA fail.
	blocker := filepath.Join(e.root, "2026-09-25-17-58", relA)
	writeFile(t, filepath.Join(blocker, "keep"), "x")

	res, err := e.tool.Pull(DefaultPullOptions(e.tool.Cfg))
	if err == nil {
		t.Fatal("partial failure must return an error")
	}
	if res.Copied != 1 || res.Deleted != 1 || len(res.Failed) != 1 || res.Failed[0] != relA || res.Ejected {
		t.Fatalf("unexpected result %+v", res)
	}
	assertOnDevice(t, e, relA, true)
	assertOnDevice(t, e, relB, false)
	if len(e.sys.ejected) != 0 {
		t.Errorf("must not eject after a failed copy, got %v", e.sys.ejected)
	}
	if len(e.sys.notes) != 1 || !strings.Contains(e.sys.notes[0], "1 failed") {
		t.Errorf("notifications: %v", e.sys.notes)
	}
	if _, err := os.Stat(e.tool.Cfg.PullLock); !os.IsNotExist(err) {
		t.Error("pull lock must be released after failure")
	}
	leftovers, _ := filepath.Glob(filepath.Join(e.root, "2026-09-25-17-58", "DJI_Audio_001", ".DJI_01_*"))
	if len(leftovers) != 0 {
		t.Errorf("temp files left behind: %v", leftovers)
	}
}

func TestPullAllCopiesFailNoDeleteNoEject(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission checks do not apply to root")
	}
	e := newEnv(t)
	dest := filepath.Join(e.root, "2026-09-25-17-58")
	if err := os.MkdirAll(dest, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dest, 0o755) })

	res, err := e.tool.Pull(DefaultPullOptions(e.tool.Cfg))
	if err == nil {
		t.Fatal("expected error when nothing could be copied")
	}
	if res.Copied != 0 || res.Deleted != 0 || len(res.Failed) != 2 || res.Ejected {
		t.Fatalf("unexpected result %+v", res)
	}
	assertOnDevice(t, e, relA, true)
	assertOnDevice(t, e, relB, true)
	if len(e.sys.ejected) != 0 {
		t.Errorf("zero copies must not eject, got %v", e.sys.ejected)
	}
}

func TestPullZeroAudioFilesNoEject(t *testing.T) {
	e := newEnv(t)
	os.Remove(filepath.Join(e.vol, relA))
	os.Remove(filepath.Join(e.vol, relB))

	opts := DefaultPullOptions(e.tool.Cfg)
	if !opts.EjectAfter || !opts.DeleteAfter {
		t.Fatal("defaults should delete and eject")
	}
	res, err := e.tool.Pull(opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Files != 0 || res.Copied != 0 || res.Ejected || len(e.sys.ejected) != 0 {
		t.Fatalf("zero audio files must leave the device mounted: %+v %v", res, e.sys.ejected)
	}
	if entries, _ := os.ReadDir(e.root); len(entries) != 0 {
		t.Error("no destination folder should be created without audio")
	}
	assertOnDevice(t, e, "DJI_Audio_001/._DJI_01_20260925_175801.WAV", true)
	assertOnDevice(t, e, "DJI_Audio_001/readme.txt", true)
	if len(e.sys.notes) != 0 {
		t.Errorf("no notification expected, got %v", e.sys.notes)
	}
}

func TestPullDeleteFailureKeepsCopy(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission checks do not apply to root")
	}
	e := newEnv(t)
	dirs := []string{filepath.Join(e.vol, "DJI_Audio_001"), filepath.Join(e.vol, "DJI_Audio_001", "sub")}
	for _, d := range dirs {
		if err := os.Chmod(d, 0o555); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, d := range dirs {
			os.Chmod(d, 0o755)
		}
	})

	res, err := e.tool.Pull(DefaultPullOptions(e.tool.Cfg))
	if err != nil {
		t.Fatalf("delete failure is not a copy failure: %v", err)
	}
	if res.Copied != 2 || res.Deleted != 0 {
		t.Fatalf("unexpected result %+v", res)
	}
	assertOnDevice(t, e, relA, true)
	assertOnDevice(t, e, relB, true)
	for _, rel := range []string{relA, relB} {
		if _, err := os.Stat(filepath.Join(res.DestDir, rel)); err != nil {
			t.Errorf("%s should be copied: %v", rel, err)
		}
	}
	if !strings.Contains(e.log.String(), "could not delete") {
		t.Errorf("log should report delete failure:\n%s", e.log)
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func TestPullConcurrentLockContention(t *testing.T) {
	e := newEnv(t)
	const workers = 8

	var wg sync.WaitGroup
	results := make([]PullResult, workers)
	errs := make([]error, workers)
	start := make(chan struct{})
	log := &syncBuffer{}
	for i := 0; i < workers; i++ {
		tool := *e.tool
		tool.Out = log
		tool.Logf = func(f string, a ...any) { fmt.Fprintf(log, f+"\n", a...) }
		wg.Add(1)
		go func(i int, tool *Tool) {
			defer wg.Done()
			<-start
			opts := DefaultPullOptions(tool.Cfg)
			opts.Stamp = fmt.Sprintf("worker-%d", i)
			results[i], errs[i] = tool.Pull(opts)
		}(i, &tool)
	}
	close(start)
	wg.Wait()

	copied := 0
	for i := range results {
		if errs[i] != nil {
			t.Errorf("worker %d: %v", i, errs[i])
		}
		copied += results[i].Copied
	}
	if copied != 2 {
		t.Fatalf("each file must be copied exactly once across workers, got %d copies\n%s", copied, log.b.String())
	}
	for _, rel := range []string{relA, relB} {
		matches, _ := filepath.Glob(filepath.Join(e.root, "worker-*", rel))
		if len(matches) != 1 {
			t.Errorf("%s landed in %d destinations: %v", rel, len(matches), matches)
		}
	}
	if len(e.sys.ejected) != 1 {
		t.Errorf("exactly one eject expected, got %v", e.sys.ejected)
	}
	if _, err := os.Stat(e.tool.Cfg.PullLock); !os.IsNotExist(err) {
		t.Error("pull lock must be released")
	}
}

func TestAcquireLockContention(t *testing.T) {
	lockDir := filepath.Join(t.TempDir(), "nested", "pull.lock")

	release, ok, err := acquireLock(lockDir)
	if err != nil || !ok {
		t.Fatalf("first acquire: ok=%v err=%v", ok, err)
	}
	if _, ok, err := acquireLock(lockDir); err != nil || ok {
		t.Fatalf("second acquire must fail while held: ok=%v err=%v", ok, err)
	}
	release()
	if _, err := os.Stat(lockDir); !os.IsNotExist(err) {
		t.Fatal("release must remove the lock dir")
	}

	for name, pid := range map[string]*string{
		"no pid file":   nil,
		"garbage pid":   ptr("not-a-pid\n"),
		"non-positive":  ptr("0\n"),
		"live pid self": ptr(fmt.Sprintf("%d\n", os.Getpid())),
	} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "pull.lock")
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if pid != nil {
				writeFile(t, filepath.Join(dir, "pid"), *pid)
			}
			if _, ok, err := acquireLock(dir); err != nil || ok {
				t.Fatalf("lock without a provably dead owner must stay held: ok=%v err=%v", ok, err)
			}
		})
	}
}

func ptr(s string) *string { return &s }

func TestPullIfPresentAbsentHasNoSideEffects(t *testing.T) {
	e := newEnv(t)
	e.tool.Cfg.MediaName = "absent"
	sleeps := 0
	e.tool.Sleep = func(time.Duration) { sleeps++ }

	opts := DefaultPullOptions(e.tool.Cfg)
	opts.IfPresent = true
	res, err := e.tool.Pull(opts)
	if err != nil {
		t.Fatalf("--if-present must succeed when absent: %v", err)
	}
	if res.Source != "" || res.Files != 0 || res.Copied != 0 {
		t.Errorf("unexpected result %+v", res)
	}
	if sleeps != 0 {
		t.Errorf("must not wait for a mount without the USB device, slept %d", sleeps)
	}
	if _, err := os.Stat(e.tool.Cfg.PullLock); !os.IsNotExist(err) {
		t.Error("absent device must not create the pull lock")
	}
	if entries, _ := os.ReadDir(e.root); len(entries) != 0 {
		t.Error("absent device must not create destination folders")
	}
	if len(e.sys.ejected) != 0 || len(e.sys.notes) != 0 {
		t.Errorf("no eject/notify expected: %v %v", e.sys.ejected, e.sys.notes)
	}
	assertOnDevice(t, e, relA, true)
	if !strings.Contains(e.log.String(), "nothing to do") {
		t.Errorf("log:\n%s", e.log)
	}

	missing := filepath.Join(e.volumes, "GONE")
	opts.Source = missing
	if _, err := e.tool.Pull(opts); err != nil {
		t.Fatalf("--if-present with a vanished --source must succeed: %v", err)
	}
	opts.IfPresent = false
	if _, err := e.tool.Pull(opts); !errors.Is(err, ErrNotMounted) {
		t.Fatalf("without --if-present a vanished --source is an error, got %v", err)
	}
}

func TestPullRejectsEmptyRoot(t *testing.T) {
	for _, root := range []string{"", "   "} {
		t.Run(fmt.Sprintf("%q", root), func(t *testing.T) {
			e := newEnv(t)
			e.tool.Cfg.Root = root

			res, err := e.tool.Pull(DefaultPullOptions(e.tool.Cfg))
			if err == nil || !strings.Contains(err.Error(), "dji.root") {
				t.Fatalf("want dji.root error, got %v", err)
			}
			if res.Copied != 0 || res.Deleted != 0 || res.Ejected || len(e.sys.ejected) != 0 {
				t.Fatalf("empty root must not touch the device: %+v %v", res, e.sys.ejected)
			}
			assertOnDevice(t, e, relA, true)
			assertOnDevice(t, e, relB, true)
			if _, err := os.Stat(e.tool.Cfg.PullLock); !os.IsNotExist(err) {
				t.Error("pull lock must be released on config error")
			}

			opts := PullOptions{Dest: t.TempDir(), Stamp: "explicit"}
			if res, err := e.tool.Pull(opts); err != nil || res.Copied != 2 {
				t.Fatalf("explicit --dest should work without dji.root: %+v %v", res, err)
			}
		})
	}
}
