package upload

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jborkowski/vmc/internal/config"
)

func TestShouldUploadForceAndCadence(t *testing.T) {
	shardDir := t.TempDir()
	cfg := &config.Config{
		ShardDir:       shardDir,
		UploadInterval: 3600,
	}

	ok, err := ShouldUpload(cfg, true)
	if err != nil || !ok {
		t.Fatalf("force should upload: ok=%v err=%v", ok, err)
	}

	ok, err = ShouldUpload(cfg, false)
	if err != nil || !ok {
		t.Fatalf("missing last_upload should allow upload: ok=%v err=%v", ok, err)
	}

	if err := MarkUploaded(cfg); err != nil {
		t.Fatalf("MarkUploaded: %v", err)
	}
	ok, err = ShouldUpload(cfg, false)
	if err != nil {
		t.Fatalf("ShouldUpload: %v", err)
	}
	if ok {
		t.Fatal("expected upload to be gated immediately after MarkUploaded")
	}

	// Stale timestamp beyond upload_interval.
	path := filepath.Join(filepath.Dir(shardDir), "last_upload")
	if err := os.WriteFile(path, []byte("1\n"), 0600); err != nil {
		t.Fatalf("write last_upload: %v", err)
	}
	ok, err = ShouldUpload(cfg, false)
	if err != nil || !ok {
		t.Fatalf("stale last_upload should allow upload: ok=%v err=%v", ok, err)
	}
}
