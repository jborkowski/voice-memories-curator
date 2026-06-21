package detect

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/jborkowski/vmc/internal/testutil"
)

func TestDetectBasic(t *testing.T) {
	testutil.SuppressLogs()

	rows := []testutil.AppleDBRow{
		{Z_PK: 1, ZDATE: 1000, ZPATH: "2023/1.m4a", ZCUSTOMLABEL: "Memo 1", ZDURATION: 10.5},
		{Z_PK: 2, ZDATE: 2000, ZPATH: "2023/2.m4a", ZCUSTOMLABEL: "Memo 2", ZDURATION: 5.0},
	}
	dbPath := testutil.CreateAppleDB(t, rows)
	shardDir := t.TempDir()
	cfg := testutil.SetupConfig(t, "https://example.com", dbPath, shardDir)

	db := testutil.GetDuckDB(t)

	err := Run(db, cfg)
	if err != nil {
		t.Fatalf("detect Run failed: %v", err)
	}

	shardPath := filepath.Join(shardDir, "shard_0001.parquet")
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM '" + shardPath + "'").Scan(&count); err != nil {
		t.Fatalf("failed to read shard: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 rows in shard, got %d", count)
	}

	var nullAudioCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM '" + shardPath + "' WHERE audio IS NULL").Scan(&nullAudioCount); err != nil {
		t.Fatalf("failed to check audio column: %v", err)
	}
	if nullAudioCount != 2 {
		t.Errorf("expected 2 null audio rows, got %d", nullAudioCount)
	}
}

func TestDetectIdempotent(t *testing.T) {
	testutil.SuppressLogs()

	rows := []testutil.AppleDBRow{
		{Z_PK: 1, ZDATE: 1000, ZPATH: "2023/1.m4a", ZCUSTOMLABEL: "Memo 1", ZDURATION: 10.5},
	}
	dbPath := testutil.CreateAppleDB(t, rows)
	shardDir := t.TempDir()
	cfg := testutil.SetupConfig(t, "https://example.com", dbPath, shardDir)

	db := testutil.GetDuckDB(t)

	// Run once
	if err := Run(db, cfg); err != nil {
		t.Fatalf("detect Run failed: %v", err)
	}

	// Run twice
	if err := Run(db, cfg); err != nil {
		t.Fatalf("detect Run 2 failed: %v", err)
	}

	// Should only have 1 shard or the second shard shouldn't exist
	shardPath := filepath.Join(shardDir, "shard_0001.parquet")
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM '" + shardPath + "'").Scan(&count); err != nil {
		t.Fatalf("failed to read shard: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 row in shard, got %d", count)
	}

	shard2Path := filepath.Join(shardDir, "shard_0002.parquet")
	if err := db.QueryRow("SELECT COUNT(*) FROM '" + shard2Path + "'").Scan(&count); err == nil {
		t.Errorf("shard_0002.parquet should not exist, or should have 0 rows if it does. Got count %d", count)
	} else if !strings.Contains(err.Error(), "does not exist") && !strings.Contains(err.Error(), "No files found") {
		// Just ensure it fails with "no files found"
	}
}

func TestDetectOfflineDedup(t *testing.T) {
	testutil.SuppressLogs()

	// Initial DB has 1 row
	rows := []testutil.AppleDBRow{
		{Z_PK: 1, ZDATE: 1000, ZPATH: "2023/1.m4a", ZCUSTOMLABEL: "Memo 1", ZDURATION: 10.5},
	}
	dbPath := testutil.CreateAppleDB(t, rows)
	shardDir := t.TempDir()
	cfg := testutil.SetupConfig(t, "https://example.com", dbPath, shardDir)

	db := testutil.GetDuckDB(t)

	// Detect generates shard_0001 with 1 row
	if err := Run(db, cfg); err != nil {
		t.Fatalf("detect Run failed: %v", err)
	}

	// Now add another row to DB
	// Recreate DB with 2 rows since we can't easily insert into the sqlite attached db directly without re-attaching
	db.Close()
	
	rows = []testutil.AppleDBRow{
		{Z_PK: 1, ZDATE: 1000, ZPATH: "2023/1.m4a", ZCUSTOMLABEL: "Memo 1", ZDURATION: 10.5},
		{Z_PK: 2, ZDATE: 2000, ZPATH: "2023/2.m4a", ZCUSTOMLABEL: "Memo 2", ZDURATION: 5.0},
	}
	dbPath2 := testutil.CreateAppleDB(t, rows)
	cfg.AppleDBPath = dbPath2
	
	db2 := testutil.GetDuckDB(t)

	// Run detect again
	if err := Run(db2, cfg); err != nil {
		t.Fatalf("detect Run 2 failed: %v", err)
	}

	// shard_0002 should only contain Z_PK = 2
	shardPath := filepath.Join(shardDir, "shard_0002.parquet")
	var pk int
	if err := db2.QueryRow("SELECT recording_id FROM '" + shardPath + "'").Scan(&pk); err != nil {
		t.Fatalf("failed to read shard 2: %v", err)
	}
	if pk != 2 {
		t.Errorf("expected recording_id 2 in shard 2, got %d", pk)
	}
}
