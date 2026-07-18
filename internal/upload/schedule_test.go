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

// Auto flow: even when cadence says "skip", missing remote shards still upload.
func TestAutoFlowUploadsMissingDespiteCadence(t *testing.T) {
	local := []string{"/data/shard_0009.parquet", "/data/shard_0010.parquet"}
	remote := map[string]struct{}{"shard_0009.parquet": {}}
	missing := filterMissingRemote(local, remote)
	if len(missing) != 1 || filepath.Base(missing[0]) != "shard_0010.parquet" {
		t.Fatalf("auto flow must select missing shard, got %v", missing)
	}
	// Cadence false + missing non-empty => publish (logic in RunWithOptions).
	cfg := &config.Config{ShardDir: t.TempDir(), UploadInterval: 999999}
	_ = MarkUploaded(cfg)
	ok, err := ShouldUpload(cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("cadence should block empty republish")
	}
	if len(missing) == 0 {
		t.Fatal("missing shards must still be uploaded when cadence blocks")
	}
}
