package djimic

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/jborkowski/vmc/internal/config"
)

// PullOptions are per-invocation overrides of the [dji] config.
type PullOptions struct {
	Source      string // volume to pull from; auto-detect when empty
	Dest        string // destination root; cfg.Root when empty
	Stamp       string // folder name; now formatted with cfg.DateFmt when empty
	DryRun      bool
	IfPresent   bool // succeed quietly when the device is absent
	DeleteAfter bool
	EjectAfter  bool
}

// DefaultPullOptions returns options seeded from cfg.
func DefaultPullOptions(cfg config.DJI) PullOptions {
	return PullOptions{DeleteAfter: cfg.DeleteAfter, EjectAfter: cfg.EjectAfter}
}

// PullResult summarizes a pull.
type PullResult struct {
	Source  string
	DestDir string
	Files   int
	Copied  int
	Deleted int
	Failed  []string
	Ejected bool
}

var skipNames = map[string]bool{".Spotlight-V100": true, ".fseventsd": true, ".Trashes": true}

var audioExts = map[string]bool{".wav": true, ".mp3": true, ".m4a": true, ".aac": true, ".flac": true, ".ogg": true}

// listAudioFiles returns regular audio files under root in byte order,
// skipping Spotlight/fsevents/Trash and AppleDouble "._" entries.
func listAudioFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root {
				return err
			}
			return nil
		}
		name := d.Name()
		if path != root && (skipNames[name] || strings.HasPrefix(name, "._")) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() && audioExts[strings.ToLower(filepath.Ext(name))] {
			files = append(files, path)
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

// acquireLock creates lockDir atomically. A lock whose recorded pid is no
// longer alive is treated as stale and replaced.
func acquireLock(lockDir string) (func(), bool, error) {
	if err := os.MkdirAll(filepath.Dir(lockDir), 0o755); err != nil {
		return nil, false, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		err := os.Mkdir(lockDir, 0o755)
		if err == nil {
			_ = os.WriteFile(filepath.Join(lockDir, "pid"), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644)
			return func() { os.RemoveAll(lockDir) }, true, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, false, err
		}
		if !lockStale(lockDir) {
			return nil, false, nil
		}
		os.RemoveAll(lockDir)
	}
	return nil, false, nil
}

func lockStale(lockDir string) bool {
	b, err := os.ReadFile(filepath.Join(lockDir, "pid"))
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return false
	}
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

// copyFile copies src to dest via a dot-prefixed temp file (ignored by the
// ingest scan) and renames it into place, preserving mode and mtime.
func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), "."+filepath.Base(dest)+".*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	_ = os.Chmod(tmpName, info.Mode().Perm())
	_ = os.Chtimes(tmpName, info.ModTime(), info.ModTime())
	return os.Rename(tmpName, dest)
}

func sameSize(src, dest string) bool {
	a, err := os.Stat(src)
	if err != nil {
		return false
	}
	b, err := os.Stat(dest)
	if err != nil || !b.Mode().IsRegular() {
		return false
	}
	return a.Size() == b.Size()
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// removeEmptyAudioDirs deletes empty top-level audio_dir_glob folders.
func (t *Tool) removeEmptyAudioDirs(vol string) {
	for _, dir := range t.AudioDirs(vol) {
		if entries, err := os.ReadDir(dir); err == nil && len(entries) == 0 {
			_ = os.Remove(dir)
		}
	}
}

// Pull copies audio from the transmitter into <dest>/<stamp>/, preserving
// paths relative to the volume root. Ejection happens only after a
// non-dry-run pull that copied at least one file with no failures.
func (t *Tool) Pull(opts PullOptions) (PullResult, error) {
	var res PullResult
	if opts.IfPresent {
		t.logf("watch tick")
	}

	source := opts.Source
	if source == "" {
		var v string
		if opts.IfPresent && t.USBPresent() {
			// Unmounts and unrelated volumes also fire WatchPaths; only wait
			// for a mount while the transmitter is actually on USB.
			v, _ = t.waitForVolume()
		} else {
			v, _ = t.FindVolume()
		}
		source = v
	}
	res.Source = source

	if info, err := os.Stat(source); source == "" || err != nil || !info.IsDir() {
		if opts.IfPresent {
			t.logf("no DJI Mic mounted; nothing to do")
			return res, nil
		}
		return res, fmt.Errorf("%w (looked for media %q, uuid %q, USB %d:%d)",
			ErrNotMounted, t.Cfg.MediaName, t.Cfg.VolumeUUID, t.Cfg.USBVendor, t.Cfg.USBProduct)
	}

	if !t.hasAudioDirs(source) {
		t.logf("no %s folders on %s (empty card; leaving mounted)", t.audioDirGlob(), source)
		return res, nil
	}

	lockDir, err := t.Cfg.PullLockPath()
	if err != nil {
		return res, err
	}
	release, ok, err := acquireLock(lockDir)
	if err != nil {
		return res, fmt.Errorf("acquire pull lock: %w", err)
	}
	if !ok {
		t.logf("another dji pull is already running")
		return res, nil
	}
	defer release()

	destRoot := opts.Dest
	if destRoot == "" {
		if destRoot, err = t.Cfg.RootPath(); err != nil {
			return res, err
		}
	} else if destRoot, err = config.ExpandHome(destRoot); err != nil {
		return res, err
	}
	stamp := opts.Stamp
	if stamp == "" {
		layout := t.Cfg.DateFmt
		if layout == "" {
			layout = "2006-01-02-15-04"
		}
		stamp = t.Now().Format(layout)
	}
	destDir := filepath.Join(destRoot, stamp)
	res.DestDir = destDir

	t.logf("source:      %s", source)
	t.logf("media name:  %s", orUnknown(t.MediaName(source)))
	t.logf("volume uuid: %s", orUnknown(t.VolumeUUID(source)))
	t.logf("destination: %s", destDir)
	if opts.DeleteAfter {
		t.logf("after copy:  delete verified files from the device")
	} else {
		t.logf("after copy:  keep files on the device")
	}
	if opts.EjectAfter {
		t.logf("after copy:  eject the disk (safe to unplug)")
	} else {
		t.logf("after copy:  leave the volume mounted")
	}

	files, err := listAudioFiles(source)
	if err != nil {
		return res, fmt.Errorf("list audio on %s: %w", source, err)
	}
	res.Files = len(files)
	if len(files) == 0 {
		t.logf("no audio files found on the device (leaving mounted)")
		return res, nil
	}

	var total int64
	for _, f := range files {
		if info, err := os.Stat(f); err == nil {
			total += info.Size()
		}
	}
	t.logf("files:       %d (%s)", len(files), humanBytes(total))

	rel := func(p string) string {
		r, err := filepath.Rel(source, p)
		if err != nil {
			return filepath.Base(p)
		}
		return r
	}

	if opts.DryRun {
		for _, f := range files {
			t.logf("would copy  %s", rel(f))
			if opts.DeleteAfter {
				t.logf("would delete %s from device", rel(f))
			}
		}
		if opts.EjectAfter {
			t.logf("would eject the disk after a successful copy")
		}
		return res, nil
	}

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return res, err
	}
	for _, src := range files {
		r := rel(src)
		dest := filepath.Join(destDir, r)
		t.logf("copying %s", r)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			res.Failed = append(res.Failed, r)
			t.logf("  failed to copy %s: %v", r, err)
			continue
		}
		if err := copyFile(src, dest); err != nil {
			res.Failed = append(res.Failed, r)
			t.logf("  failed to copy %s: %v", r, err)
			continue
		}
		if !sameSize(src, dest) {
			res.Failed = append(res.Failed, r)
			t.logf("  size mismatch after copy: %s", r)
			continue
		}
		res.Copied++
		if opts.DeleteAfter {
			if err := os.Remove(src); err != nil {
				t.logf("  copied but could not delete from device: %s", r)
			} else {
				res.Deleted++
			}
		}
	}
	if opts.DeleteAfter {
		t.removeEmptyAudioDirs(source)
	}

	t.logf("done. copied %d / %d files into %s", res.Copied, len(files), destDir)
	if opts.DeleteAfter {
		t.logf("deleted %d files from the device", res.Deleted)
	}

	if len(res.Failed) > 0 {
		t.logf("failed:")
		for _, r := range res.Failed {
			t.logf("  %s", r)
		}
		t.notify(fmt.Sprintf("Pull finished with %d failed files", len(res.Failed)))
		return res, fmt.Errorf("dji pull: %d of %d files failed", len(res.Failed), len(files))
	}

	t.notify(fmt.Sprintf("Pulled %d files into %s", res.Copied, stamp))
	// Eject only after a real copy: empty remounts and no-audio ticks stay mounted.
	if opts.EjectAfter && res.Copied > 0 {
		if err := t.EjectVolume(source); err == nil {
			res.Ejected = true
		}
	}
	return res, nil
}
