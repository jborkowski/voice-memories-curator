package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, home, body string) {
	t.Helper()
	dir := filepath.Join(home, ".config", "vmc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDJIDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("HF_TOKEN", "")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	d := cfg.DJI
	if d.Enabled {
		t.Error("DJI must be disabled by default")
	}
	if d.Root != "" || d.MediaName != "" || d.VolumeUUID != "" || d.USBVendor != 0 || d.USBProduct != 0 {
		t.Errorf("personal/device fields must be empty in defaults: %+v", d)
	}
	if d.DirGlob != "????-??-??-??-??/DJI_Audio_*" || d.MinAgeSeconds != 120 {
		t.Errorf("unexpected safe defaults: %+v", d)
	}
	if _, err := d.RootPath(); err == nil {
		t.Error("RootPath must error when root is unset")
	}
}

func TestDJIPartialOverrideKeepsDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("HF_TOKEN", "")
	writeConfig(t, home, "hf_repo = \"me/voice\"\n\n[dji]\nenabled = true\nroot = \"~/inbox\"\ndevice_label = \"lab mic\"\n")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HFRepo != "me/voice" {
		t.Errorf("HFRepo = %q", cfg.HFRepo)
	}
	if !cfg.DJI.Enabled || cfg.DJI.Root != "~/inbox" || cfg.DJI.DeviceLabel != "lab mic" {
		t.Errorf("override not applied: %+v", cfg.DJI)
	}
	if cfg.DJI.MinAgeSeconds != 120 || cfg.DJI.DirGlob == "" {
		t.Errorf("unset keys lost defaults: %+v", cfg.DJI)
	}
	root, err := cfg.DJI.RootPath()
	if err != nil {
		t.Fatal(err)
	}
	if root != filepath.Join(home, "inbox") {
		t.Errorf("RootPath = %q", root)
	}
}

func TestDJIRootRejectsEmpty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, root := range []string{"", " ", "\t\n"} {
		d := DJI{Enabled: true, Root: root}
		if _, err := d.RootPath(); err == nil {
			t.Errorf("RootPath(%q) must error", root)
		}
		if _, err := d.Pattern(); err == nil {
			t.Errorf("Pattern(%q) must error", root)
		}
	}
}

func TestDJIPullLockPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := DJI{}.PullLockPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".local", "state", "vmc", "dji-pull.lock"); got != want {
		t.Errorf("default PullLockPath = %q, want %q", got, want)
	}
	got, err = DJI{PullLock: "~/x/p.lock"}.PullLockPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "x", "p.lock"); got != want {
		t.Errorf("PullLockPath = %q, want %q", got, want)
	}
}

func TestExpandHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cases := map[string]string{
		"~":        home,
		"~/a/b":    filepath.Join(home, "a", "b"),
		"/abs/x~y": "/abs/x~y",
		"rel/path": "rel/path",
		"~other/x": "~other/x",
	}
	for in, want := range cases {
		got, err := ExpandHome(in)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("ExpandHome(%q) = %q, want %q", in, got, want)
		}
	}
}
