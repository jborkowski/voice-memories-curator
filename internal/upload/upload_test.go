package upload

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jborkowski/vmc/internal/detect"
	"github.com/jborkowski/vmc/internal/process"
	"github.com/jborkowski/vmc/internal/testutil"
)

func TestUploadReadyCheck(t *testing.T) {
	testutil.SuppressLogs()

	rows := []testutil.AppleDBRow{
		{Z_PK: 1, ZDATE: 1000, ZPATH: "2023/1.m4a", ZCUSTOMLABEL: "Memo 1", ZDURATION: 1.0},
	}
	dbPath := testutil.CreateAppleDB(t, rows)
	testutil.SetupAudioFiles(t, dbPath, rows)

	shardDir := t.TempDir()
	cfg := testutil.SetupConfig(t, "https://example.com", dbPath, shardDir)

	db := testutil.GetDuckDB(t)

	if err := detect.Run(db, cfg); err != nil {
		t.Fatalf("detect Run failed: %v", err)
	}

	shardPath := filepath.Join(shardDir, "shard_0001.parquet")

	ready, err := isShardReady(db, shardPath)
	if err != nil {
		t.Fatalf("isShardReady failed: %v", err)
	}
	if ready {
		t.Errorf("expected shard to NOT be ready (audio is null)")
	}

	if err := process.Run(db, cfg); err != nil {
		t.Fatalf("process Run failed: %v", err)
	}

	ready, err = isShardReady(db, shardPath)
	if err != nil {
		t.Fatalf("isShardReady failed: %v", err)
	}
	if !ready {
		t.Errorf("expected shard to be ready (audio is filled)")
	}
}

func TestUploadOffline(t *testing.T) {
	testutil.SuppressLogs()

	rows := []testutil.AppleDBRow{
		{Z_PK: 1, ZDATE: 1000, ZPATH: "2023/1.m4a", ZCUSTOMLABEL: "Memo 1", ZDURATION: 1.0},
	}
	dbPath := testutil.CreateAppleDB(t, rows)
	testutil.SetupAudioFiles(t, dbPath, rows)

	shardDir := t.TempDir()
	cfg := testutil.SetupConfig(t, "http://localhost:12345/not-exist", dbPath, shardDir)

	db := testutil.GetDuckDB(t)

	_ = detect.Run(db, cfg)
	_ = process.Run(db, cfg)

	if err := RunWithOptions(db, cfg, true); err != nil {
		t.Fatalf("expected clean exit for offline upload, got error: %v", err)
	}

	shardPath := filepath.Join(shardDir, "shard_0001.parquet")
	if _, err := os.Stat(shardPath); os.IsNotExist(err) {
		t.Errorf("expected shard to remain local after offline upload attempt")
	}
}
