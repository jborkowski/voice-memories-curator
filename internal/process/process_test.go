package process

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jborkowski/vmc/internal/detect"
	"github.com/jborkowski/vmc/internal/testutil"
)

func checkFfmpeg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
}

func TestProcessBasic(t *testing.T) {
	checkFfmpeg(t)
	// testutil.SuppressLogs()

	rows := []testutil.AppleDBRow{
		{Z_PK: 1, ZDATE: 1000, ZPATH: "2023/1.m4a", ZCUSTOMLABEL: "Memo 1", ZDURATION: 1.0},
	}
	dbPath := testutil.CreateAppleDB(t, rows)
	testutil.SetupAudioFiles(t, dbPath, rows)

	shardDir := t.TempDir()
	cfg := testutil.SetupConfig(t, "https://example.com", dbPath, shardDir)

	db := testutil.GetDuckDB(t)

	// Run detect to generate shard
	if err := detect.Run(db, cfg); err != nil {
		t.Fatalf("detect Run failed: %v", err)
	}

	// Run process
	if err := Run(db, cfg); err != nil {
		t.Fatalf("process Run failed: %v", err)
	}

	shardPath := filepath.Join(shardDir, "shard_0001.parquet")
	var nullAudioCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM '" + shardPath + "' WHERE audio IS NULL").Scan(&nullAudioCount); err != nil {
		t.Fatalf("failed to read shard: %v", err)
	}
	if nullAudioCount != 0 {
		t.Errorf("expected 0 null audio rows, got %d", nullAudioCount)
	}

	var hasAudioCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM '" + shardPath + "' WHERE audio IS NOT NULL").Scan(&hasAudioCount); err != nil {
		t.Fatalf("failed to read shard: %v", err)
	}
	if hasAudioCount != 1 {
		t.Errorf("expected 1 row with audio, got %d", hasAudioCount)
	}
}

func TestProcessMissingFile(t *testing.T) {
	checkFfmpeg(t)
	// testutil.SuppressLogs()

	rows := []testutil.AppleDBRow{
		{Z_PK: 1, ZDATE: 1000, ZPATH: "2023/missing.m4a", ZCUSTOMLABEL: "Memo 1", ZDURATION: 1.0},
	}
	dbPath := testutil.CreateAppleDB(t, rows)
	// Do not SetupAudioFiles to simulate missing file

	shardDir := t.TempDir()
	cfg := testutil.SetupConfig(t, "https://example.com", dbPath, shardDir)

	db := testutil.GetDuckDB(t)

	// Run detect
	if err := detect.Run(db, cfg); err != nil {
		t.Fatalf("detect Run failed: %v", err)
	}

	// Run process - shouldn't crash
	if err := Run(db, cfg); err != nil {
		t.Fatalf("process Run failed: %v", err)
	}

	// Audio should remain NULL
	shardPath := filepath.Join(shardDir, "shard_0001.parquet")
	var nullAudioCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM '" + shardPath + "' WHERE audio IS NULL").Scan(&nullAudioCount); err != nil {
		t.Fatalf("failed to read shard: %v", err)
	}
	if nullAudioCount != 1 {
		t.Errorf("expected 1 null audio row, got %d", nullAudioCount)
	}
}

func TestProcessCrashResume(t *testing.T) {
	checkFfmpeg(t)
	// testutil.SuppressLogs()

	rows := []testutil.AppleDBRow{
		{Z_PK: 1, ZDATE: 1000, ZPATH: "2023/1.m4a", ZCUSTOMLABEL: "Memo 1", ZDURATION: 1.0},
		{Z_PK: 2, ZDATE: 2000, ZPATH: "2023/2.m4a", ZCUSTOMLABEL: "Memo 2", ZDURATION: 1.0},
		{Z_PK: 3, ZDATE: 3000, ZPATH: "2023/3.m4a", ZCUSTOMLABEL: "Memo 3", ZDURATION: 1.0},
	}
	dbPath := testutil.CreateAppleDB(t, rows)
	testutil.SetupAudioFiles(t, dbPath, rows)

	shardDir := t.TempDir()
	cfg := testutil.SetupConfig(t, "https://example.com", dbPath, shardDir)

	db := testutil.GetDuckDB(t)

	// Run detect
	if err := detect.Run(db, cfg); err != nil {
		t.Fatalf("detect Run failed: %v", err)
	}

	shardPath := filepath.Join(shardDir, "shard_0001.parquet")

	// Simulate partial processing: Rewrite the shard so recording 1 has mock audio (non-null blob), others are NULL
	rewriteQuery := `
		COPY (
			SELECT 
				recording_id,
				CASE WHEN recording_id = 1 THEN CAST('mockaudio' AS BLOB) ELSE audio END AS audio,
				audio_original,
				audio_path, title, created_at, duration_seconds,
				transcription, latitude, longitude,
				place_name, device, folder
			FROM '` + strings.ReplaceAll(shardPath, "'", "''") + `'
		) TO '` + strings.ReplaceAll(shardPath+".tmp", "'", "''") + `' (FORMAT PARQUET)
	`
	if _, err := db.Exec(rewriteQuery); err != nil {
		t.Fatalf("failed to rewrite shard: %v", err)
	}
	if err := os.Rename(shardPath+".tmp", shardPath); err != nil {
		t.Fatalf("failed to replace shard via rename: %v", err)
	}

	// Verify partial state
	var nullAudioCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM '" + shardPath + "' WHERE audio IS NULL").Scan(&nullAudioCount); err != nil {
		t.Fatalf("failed to read shard: %v", err)
	}
	if nullAudioCount != 2 {
		t.Fatalf("setup failed: expected 2 null audio rows, got %d", nullAudioCount)
	}

	// Run process again
	if err := Run(db, cfg); err != nil {
		t.Fatalf("process Run failed: %v", err)
	}

	// Verify full state
	if err := db.QueryRow("SELECT COUNT(*) FROM '" + shardPath + "' WHERE audio IS NULL").Scan(&nullAudioCount); err != nil {
		t.Fatalf("failed to read shard: %v", err)
	}
	if nullAudioCount != 0 {
		t.Errorf("expected 0 null audio rows after resume, got %d", nullAudioCount)
	}
}
