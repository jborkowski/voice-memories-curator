package detect

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jborkowski/vmc/internal/config"
)

const defaultDJIDeviceLabel = "DJI Mic"

const createdAtLayout = "2006-01-02T15:04:05Z"

var errDJILocked = errors.New("dji pull in progress")

// DJI Mic names recordings DJI_NN_YYYYMMDD_HHMMSS.WAV using its own
// (phone-synced, local-time) clock.
var djiNameRe = regexp.MustCompile(`^DJI_\d+_(\d{8})_(\d{6})\.[wW][aA][vV]$`)

type djiRecording struct {
	ID        int64
	AudioPath string
	Folder    string
	CreatedAt string
	Duration  float64
}

func djiDeviceLabel(d config.DJI) string {
	if d.DeviceLabel == "" {
		return defaultDJIDeviceLabel
	}
	return d.DeviceLabel
}

// scanDJI lists settled DJI WAVs under Root/DirGlob, oldest first.
func scanDJI(d config.DJI, now time.Time) ([]djiRecording, error) {
	// `vmc dji pull` holds this lock while copying into Root; files are not
	// stable until it is gone.
	lockPath, err := d.PullLockPath()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(lockPath); err == nil {
		return nil, errDJILocked
	}

	root, err := d.RootPath()
	if err != nil {
		return nil, fmt.Errorf("resolve dji root: %w", err)
	}
	pattern, err := d.Pattern()
	if err != nil {
		return nil, fmt.Errorf("resolve dji pattern: %w", err)
	}
	dirs, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("bad dji dir_glob %q: %w", d.DirGlob, err)
	}

	minAge := time.Duration(d.MinAgeSeconds) * time.Second
	var recs []djiRecording
	var unsettled, evicted, unparsable int
	for _, dir := range dirs {
		if strings.HasPrefix(filepath.Base(dir), ".") {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			slog.Warn("failed to read dji directory", "dir", dir, "error", err)
			continue
		}
		for _, e := range entries {
			name := e.Name()
			// Dotfiles cover rsync temps (.name.XXXXXX) and legacy .icloud stubs.
			if e.IsDir() || strings.HasPrefix(name, ".") || !strings.EqualFold(filepath.Ext(name), ".wav") {
				continue
			}
			path := filepath.Join(dir, name)
			info, err := os.Stat(path)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			if isEvicted(info) {
				evicted++
				continue
			}
			if now.Sub(fileCtime(info)) < minAge {
				unsettled++
				continue
			}
			created, ok := parseDJIFilename(name)
			if !ok {
				unparsable++
				slog.Debug("skipping dji file with unrecognized name", "path", path)
				continue
			}
			dur, err := wavDuration(path)
			if err != nil {
				slog.Warn("skipping dji file with unreadable WAV header", "path", path, "error", err)
				continue
			}
			folder, err := filepath.Rel(root, dir)
			if err != nil {
				folder = filepath.Base(dir)
			}
			recs = append(recs, djiRecording{
				AudioPath: path,
				Folder:    filepath.ToSlash(folder),
				CreatedAt: created.UTC().Format(createdAtLayout),
				Duration:  dur,
			})
		}
	}

	sort.SliceStable(recs, func(i, j int) bool {
		if recs[i].CreatedAt != recs[j].CreatedAt {
			return recs[i].CreatedAt < recs[j].CreatedAt
		}
		return recs[i].AudioPath < recs[j].AudioPath
	})

	slog.Debug("dji scan complete",
		slog.Int("dirs", len(dirs)),
		slog.Int("candidates", len(recs)),
		slog.Int("unsettled", unsettled),
		slog.Int("evicted", evicted),
		slog.Int("unparsable", unparsable),
	)
	return recs, nil
}

func parseDJIFilename(name string) (time.Time, bool) {
	m := djiNameRe.FindStringSubmatch(name)
	if m == nil {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("20060102150405", m[1]+m[2], time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// wavDuration reads RIFF/RF64 fmt+data chunks and returns data bytes / byte rate.
func wavDuration(path string) (float64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}

	var hdr [12]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		return 0, fmt.Errorf("read RIFF header: %w", err)
	}
	kind := string(hdr[0:4])
	if (kind != "RIFF" && kind != "RF64") || string(hdr[8:12]) != "WAVE" {
		return 0, fmt.Errorf("not a WAVE file")
	}

	var byteRate uint32
	var ds64DataSize uint64
	offset := int64(12)
	for {
		var ch [8]byte
		if _, err := io.ReadFull(f, ch[:]); err != nil {
			return 0, fmt.Errorf("no data chunk: %w", err)
		}
		id := string(ch[0:4])
		size := binary.LittleEndian.Uint32(ch[4:8])
		body := offset + 8

		switch id {
		case "ds64":
			var b [16]byte
			if _, err := io.ReadFull(f, b[:]); err != nil {
				return 0, fmt.Errorf("read ds64 chunk: %w", err)
			}
			ds64DataSize = binary.LittleEndian.Uint64(b[8:16])
		case "fmt ":
			var b [16]byte
			if size < 16 {
				return 0, fmt.Errorf("fmt chunk too short (%d bytes)", size)
			}
			if _, err := io.ReadFull(f, b[:]); err != nil {
				return 0, fmt.Errorf("read fmt chunk: %w", err)
			}
			byteRate = binary.LittleEndian.Uint32(b[8:12])
		case "data":
			if byteRate == 0 {
				return 0, fmt.Errorf("data chunk before fmt chunk or zero byte rate")
			}
			dataSize := uint64(size)
			if kind == "RF64" && size == 0xFFFFFFFF {
				dataSize = ds64DataSize
			}
			// Unfinalized headers report 0 or oversize; trust the bytes on disk.
			if remaining := info.Size() - body; remaining >= 0 && (dataSize == 0 || dataSize > uint64(remaining)) {
				dataSize = uint64(remaining)
			}
			return float64(dataSize) / float64(byteRate), nil
		}

		offset = body + int64(size) + int64(size&1)
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return 0, err
		}
	}
}

func djiDedupKey(device, createdAt string, duration float64) string {
	return fmt.Sprintf("%s|%s|%.3f", device, createdAt, duration)
}

// newDJIRecordings drops recordings already present in known rows (matched
// on device, created_at, duration_seconds) or repeated within recs itself.
func newDJIRecordings(recs []djiRecording, known []knownRow, device string) []djiRecording {
	seen := make(map[string]bool, len(known))
	for _, k := range known {
		if k.Device.Valid && k.CreatedAt.Valid && k.Duration.Valid {
			seen[djiDedupKey(k.Device.String, k.CreatedAt.String, k.Duration.Float64)] = true
		}
	}
	var out []djiRecording
	for _, r := range recs {
		key := djiDedupKey(device, r.CreatedAt, r.Duration)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
	}
	return out
}

// assignDJIIDs gives recs consecutive slot IDs in slot, after any DJI IDs
// already used there. Recordings that do not fit in the slot are dropped
// and picked up once a newer Apple memo opens the next slot.
func assignDJIIDs(recs []djiRecording, knownIDs []int64, slot int64) []djiRecording {
	used := HighestDJIIndex(knownIDs, slot)
	var out []djiRecording
	for i, r := range recs {
		n, err := NextDJIIndex(used)
		if err != nil {
			slog.Warn("dji slot full, deferring remaining recordings", "slot", slot, "deferred", len(recs)-i)
			break
		}
		id, err := EncodeDJI(slot, n)
		if err != nil {
			slog.Warn("failed to encode dji id", "slot", slot, "n", n, "error", err)
			break
		}
		used = n
		r.ID = id
		out = append(out, r)
	}
	return out
}
