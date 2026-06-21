package upload

import (
	"net/http"
	"net/http/httptest"
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
	// Set an invalid URL to simulate offline/failure
	cfg := testutil.SetupConfig(t, "http://localhost:12345/not-exist", dbPath, shardDir)

	db := testutil.GetDuckDB(t)

	detect.Run(db, cfg)
	process.Run(db, cfg)

	// Run upload, it should not fail but simply return cleanly (logs "offline")
	if err := Run(db, cfg); err != nil {
		t.Fatalf("expected clean exit for offline upload, got error: %v", err)
	}

	// Shard should still exist
	shardPath := filepath.Join(shardDir, "shard_0001.parquet")
	if _, err := os.Stat(shardPath); os.IsNotExist(err) {
		t.Errorf("expected shard to remain local after offline upload attempt")
	}
}

func TestUploadSuccess(t *testing.T) {
	testutil.SuppressLogs()

	uploadCalled := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "HEAD" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method == "POST" && r.URL.Path == "/api/datasets/test/repo/upload/main/data/shard_0001.parquet" {
			uploadCalled = true
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	rows := []testutil.AppleDBRow{
		{Z_PK: 1, ZDATE: 1000, ZPATH: "2023/1.m4a", ZCUSTOMLABEL: "Memo 1", ZDURATION: 1.0},
	}
	dbPath := testutil.CreateAppleDB(t, rows)
	testutil.SetupAudioFiles(t, dbPath, rows)

	shardDir := t.TempDir()
	cfg := testutil.SetupConfig(t, ts.URL, dbPath, shardDir)

	db := testutil.GetDuckDB(t)

	detect.Run(db, cfg)
	process.Run(db, cfg)

	if err := Run(db, cfg); err != nil {
		t.Fatalf("upload Run failed: %v", err)
	}

	if !uploadCalled {
		t.Errorf("expected upload API to be called")
	}

	shardPath := filepath.Join(shardDir, "shard_0001.parquet")
	if _, err := os.Stat(shardPath); !os.IsNotExist(err) {
		t.Errorf("expected shard to be deleted after successful upload")
	}
}
