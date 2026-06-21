package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/jborkowski/vmc/internal/detect"
	"github.com/jborkowski/vmc/internal/process"
	"github.com/jborkowski/vmc/internal/testutil"
	"github.com/jborkowski/vmc/internal/upload"
)

func TestEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	testutil.SuppressLogs()

	uploadCalled := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "HEAD" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method == "POST" {
			uploadCalled = true
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	rows := []testutil.AppleDBRow{
		{Z_PK: 1, ZDATE: 1000, ZPATH: "2023/1.m4a", ZCUSTOMLABEL: "Memo 1", ZDURATION: 1.0},
		{Z_PK: 2, ZDATE: 2000, ZPATH: "2023/2.m4a", ZCUSTOMLABEL: "Memo 2", ZDURATION: 2.0},
	}
	dbPath := testutil.CreateAppleDB(t, rows)
	testutil.SetupAudioFiles(t, dbPath, rows)

	shardDir := t.TempDir()
	cfg := testutil.SetupConfig(t, ts.URL, dbPath, shardDir)

	db := testutil.GetDuckDB(t)

	// Phase 1: Detect
	if err := detect.Run(db, cfg); err != nil {
		t.Fatalf("detect failed: %v", err)
	}

	shardPath := filepath.Join(shardDir, "shard_0001.parquet")
	if _, err := os.Stat(shardPath); os.IsNotExist(err) {
		t.Fatalf("expected shard to be created")
	}

	// Phase 2: Process
	if err := process.Run(db, cfg); err != nil {
		t.Fatalf("process failed: %v", err)
	}

	// Phase 3: Upload
	if err := upload.Run(db, cfg); err != nil {
		t.Fatalf("upload failed: %v", err)
	}

	if !uploadCalled {
		t.Errorf("expected upload API to be called")
	}

	if _, err := os.Stat(shardPath); !os.IsNotExist(err) {
		t.Errorf("expected shard to be cleaned up after upload")
	}
}
