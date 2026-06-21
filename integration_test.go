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
	if _, err := exec.LookPath("git-xet"); err != nil {
		t.Skip("git-xet not installed")
	}

	token := os.Getenv("VMC_TEST_HF_TOKEN")
	repo := os.Getenv("VMC_TEST_HF_REPO")
	if token == "" || repo == "" {
		t.Skip("VMC_TEST_HF_TOKEN and VMC_TEST_HF_REPO required for e2e test")
	}
	testutil.SuppressLogs()

	rows := []testutil.AppleDBRow{
		{Z_PK: 1, ZDATE: 1000, ZPATH: "2023/1.m4a", ZCUSTOMLABEL: "Memo 1", ZDURATION: 1.0},
		{Z_PK: 2, ZDATE: 2000, ZPATH: "2023/2.m4a", ZCUSTOMLABEL: "Memo 2", ZDURATION: 2.0},
	}
	dbPath := testutil.CreateAppleDB(t, rows)
	testutil.SetupAudioFiles(t, dbPath, rows)

	shardDir := t.TempDir()
	cfg := testutil.SetupConfig(t, "https://huggingface.co", dbPath, shardDir)
	cfg.HFToken = token
	cfg.HFRepo = repo

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

	if _, err := os.Stat(shardPath); !os.IsNotExist(err) {
		// Debug: check what happened
		t.Logf("shard still exists at %s (err=%v)", shardPath, err)
		t.Errorf("expected shard to be cleaned up after upload")
	}
}

func TestEndToEndOffline(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	if _, err := exec.LookPath("git-xet"); err != nil {
		t.Skip("git-xet not installed")
	}
	testutil.SuppressLogs()

	// Mock server that's unreachable for connectivity check
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	ts.Close() // close immediately to simulate offline

	rows := []testutil.AppleDBRow{
		{Z_PK: 1, ZDATE: 1000, ZPATH: "2023/1.m4a", ZCUSTOMLABEL: "Memo 1", ZDURATION: 1.0},
		{Z_PK: 2, ZDATE: 2000, ZPATH: "2023/2.m4a", ZCUSTOMLABEL: "Memo 2", ZDURATION: 2.0},
	}
	dbPath := testutil.CreateAppleDB(t, rows)
	testutil.SetupAudioFiles(t, dbPath, rows)

	shardDir := t.TempDir()
	cfg := testutil.SetupConfig(t, ts.URL, dbPath, shardDir)
	cfg.HFToken = "fake-token"

	db := testutil.GetDuckDB(t)

	if err := detect.Run(db, cfg); err != nil {
		t.Fatalf("detect failed: %v", err)
	}

	if err := process.Run(db, cfg); err != nil {
		t.Fatalf("process failed: %v", err)
	}

	// Upload should exit cleanly when offline
	if err := upload.Run(db, cfg); err != nil {
		t.Fatalf("upload should not error when offline: %v", err)
	}

	// Shard should still exist
	shardPath := filepath.Join(shardDir, "shard_0001.parquet")
	if _, err := os.Stat(shardPath); os.IsNotExist(err) {
		t.Errorf("expected shard to remain local when offline")
	}
}
