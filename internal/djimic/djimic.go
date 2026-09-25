// Package djimic pulls recordings off a DJI Wireless Mic transmitter that
// mounts as USB mass storage, and manages a launchd watcher for it.
//
// Device fingerprints (media name, volume UUID, USB ids) come only from the
// operator's local config; any one match is enough.
package djimic

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jborkowski/vmc/internal/config"
)

const notifyTitle = "vmc dji"

// ErrNotMounted reports that no configured transmitter volume is mounted.
var ErrNotMounted = errors.New("DJI Mic is not mounted")

// Tool runs DJI Mic operations against one config.
type Tool struct {
	Cfg        config.DJI
	Sys        System
	VolumesDir string
	Out        io.Writer
	Logf       func(format string, args ...any)
	Now        func() time.Time
	Sleep      func(time.Duration)
}

// New returns a Tool using the real system and /Volumes.
func New(cfg config.DJI, logf func(string, ...any)) *Tool {
	return &Tool{
		Cfg:        cfg,
		Sys:        ExecSystem{},
		VolumesDir: "/Volumes",
		Out:        os.Stdout,
		Logf:       logf,
		Now:        time.Now,
		Sleep:      time.Sleep,
	}
}

func (t *Tool) logf(format string, args ...any) {
	if t.Logf != nil {
		t.Logf(format, args...)
	}
}

func (t *Tool) notify(msg string) {
	if t.Cfg.Notify {
		t.Sys.Notify(notifyTitle, msg)
	}
}

func (t *Tool) audioDirGlob() string {
	if t.Cfg.AudioDirGlob == "" {
		return "DJI_Audio_*"
	}
	return t.Cfg.AudioDirGlob
}

// diskField extracts "Key: value" from diskutil info output.
func diskField(info, key string) string {
	for _, line := range strings.Split(info, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(k) == key {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// wholeDisk returns the parent disk identifier of a mounted volume.
func (t *Tool) wholeDisk(vol string) string {
	info, err := t.Sys.DiskInfo(vol)
	if err != nil {
		return ""
	}
	return diskField(info, "Part of Whole")
}

// MediaName returns the device media name of the disk backing vol.
func (t *Tool) MediaName(vol string) string {
	parent := t.wholeDisk(vol)
	if parent == "" {
		return ""
	}
	info, err := t.Sys.DiskInfo(parent)
	if err != nil {
		return ""
	}
	return diskField(info, "Device / Media Name")
}

// VolumeUUID returns the volume UUID of vol.
func (t *Tool) VolumeUUID(vol string) string {
	info, err := t.Sys.DiskInfo(vol)
	if err != nil {
		return ""
	}
	return diskField(info, "Volume UUID")
}

var (
	ioregVendorRe  = regexp.MustCompile(`"idVendor"\s*=\s*(\d+)`)
	ioregProductRe = regexp.MustCompile(`"idProduct"\s*=\s*(\d+)`)
)

// USBPresent reports whether a USB device with the configured vendor and
// product ids is attached. Unset ids never match.
func (t *Tool) USBPresent() bool {
	if t.Cfg.USBVendor == 0 || t.Cfg.USBProduct == 0 {
		return false
	}
	out, err := t.Sys.USBRegistry()
	if err != nil {
		return false
	}
	vendor, product := strconv.Itoa(t.Cfg.USBVendor), strconv.Itoa(t.Cfg.USBProduct)
	for _, block := range strings.Split(out, "+-o ") {
		v := ioregVendorRe.FindStringSubmatch(block)
		p := ioregProductRe.FindStringSubmatch(block)
		if v != nil && p != nil && v[1] == vendor && p[1] == product {
			return true
		}
	}
	return false
}

// AudioDirs lists top-level directories on vol matching audio_dir_glob.
func (t *Tool) AudioDirs(vol string) []string {
	entries, err := os.ReadDir(vol)
	if err != nil {
		return nil
	}
	var dirs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if ok, _ := filepath.Match(t.audioDirGlob(), e.Name()); ok {
			dirs = append(dirs, filepath.Join(vol, e.Name()))
		}
	}
	return dirs
}

func (t *Tool) hasAudioDirs(vol string) bool {
	return len(t.AudioDirs(vol)) > 0
}

func (t *Tool) volumes() []string {
	entries, err := os.ReadDir(t.VolumesDir)
	if err != nil {
		return nil
	}
	var vols []string
	for _, e := range entries {
		p := filepath.Join(t.VolumesDir, e.Name())
		if info, err := os.Stat(p); err == nil && info.IsDir() {
			vols = append(vols, p)
		}
	}
	sort.Strings(vols)
	return vols
}

// FindVolume returns the mounted transmitter volume, matching media name,
// then volume UUID, then USB ids plus audio_dir_glob folders.
func (t *Tool) FindVolume() (string, error) {
	vols := t.volumes()
	if t.Cfg.MediaName != "" {
		for _, v := range vols {
			if t.MediaName(v) == t.Cfg.MediaName {
				return v, nil
			}
		}
	}
	if t.Cfg.VolumeUUID != "" {
		for _, v := range vols {
			if strings.EqualFold(t.VolumeUUID(v), t.Cfg.VolumeUUID) {
				return v, nil
			}
		}
	}
	// USB serial is not unique; ids plus audio folders together are.
	if t.USBPresent() {
		for _, v := range vols {
			if t.hasAudioDirs(v) {
				return v, nil
			}
		}
	}
	return "", ErrNotMounted
}

func (t *Tool) waitForVolume() (string, error) {
	for i := 0; i < t.Cfg.MountWaitSecs; i++ {
		if v, err := t.FindVolume(); err == nil {
			return v, nil
		}
		t.Sleep(time.Second)
	}
	return "", ErrNotMounted
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// Detect prints the matched volume and fingerprints. It returns
// ErrNotMounted when the transmitter is absent.
func (t *Tool) Detect() error {
	usb := "no"
	if t.USBPresent() {
		usb = fmt.Sprintf("yes (vendor=%d product=%d)", t.Cfg.USBVendor, t.Cfg.USBProduct)
	}
	vol, err := t.FindVolume()
	if err != nil {
		fmt.Fprintln(t.Out, "mounted:     no")
		fmt.Fprintf(t.Out, "usb present: %s\n", usb)
		fmt.Fprintf(t.Out, "media name:  %s\n", t.Cfg.MediaName)
		fmt.Fprintf(t.Out, "volume uuid: %s\n", t.Cfg.VolumeUUID)
		return err
	}
	fmt.Fprintln(t.Out, "mounted:     yes")
	fmt.Fprintf(t.Out, "volume:      %s\n", vol)
	fmt.Fprintf(t.Out, "media name:  %s\n", orUnknown(t.MediaName(vol)))
	fmt.Fprintf(t.Out, "volume uuid: %s\n", orUnknown(t.VolumeUUID(vol)))
	fmt.Fprintf(t.Out, "usb present: %s\n", usb)
	fmt.Fprintf(t.Out, "audio dirs:  %s\n", strings.Join(t.AudioDirs(vol), " "))
	return nil
}

// EjectVolume ejects the whole disk backing vol so it is safe to unplug.
func (t *Tool) EjectVolume(vol string) error {
	if vol == "" {
		return errors.New("no volume to eject")
	}
	target := t.wholeDisk(vol)
	if target == "" {
		target = vol
	}
	t.Sys.Sync()
	if err := t.Sys.EjectDisk(target); err != nil {
		t.logf("could not eject %s", target)
		return err
	}
	t.logf("ejected %s — safe to unplug", target)
	t.notify("Safe to unplug the DJI Mic")
	return nil
}

// Eject finds the transmitter and ejects it.
func (t *Tool) Eject() error {
	vol, err := t.FindVolume()
	if err != nil {
		return err
	}
	return t.EjectVolume(vol)
}
