package testutil

import (
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jborkowski/vmc/internal/config"
	_ "github.com/marcboeker/go-duckdb"
)

type AppleDBRow struct {
	Z_PK          int
	ZDATE         float64
	ZPATH         string
	ZCUSTOMLABEL  string
	ZDURATION     float64
}

func SuppressLogs() {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func SetupConfig(t *testing.T, hfURL, appleDBPath, shardDir string) *config.Config {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.HFToken = "test-token"
	cfg.HFRepo = "test/repo"
	cfg.HFBaseURL = hfURL
	cfg.AppleDBPath = appleDBPath
	cfg.ShardDir = shardDir
	return cfg
}

func CreateAppleDB(t *testing.T, rows []AppleDBRow) string {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "CloudRecordings.db")

	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatalf("failed to open duckdb: %v", err)
	}
	defer db.Close()

	_, err = db.Exec(fmt.Sprintf("ATTACH '%s' AS testdb (TYPE sqlite)", strings.ReplaceAll(dbPath, "'", "''")))
	if err != nil {
		t.Fatalf("failed to attach sqlite db: %v", err)
	}

	createStmt := `
CREATE TABLE testdb.ZCLOUDRECORDING (
    Z_PK          INTEGER PRIMARY KEY,
    ZDATE         REAL,
    ZPATH         TEXT,
    ZCUSTOMLABEL  TEXT,
    ZENCRYPTEDTITLE TEXT,
    ZDURATION     REAL,
    ZTRANSCRIPTION TEXT,
    ZLATITUDE     REAL,
    ZLONGITUDE    REAL,
    ZPLACENAME    TEXT,
    ZDEVICE       TEXT,
    ZFOLDER       TEXT
);
`
	if _, err := db.Exec(createStmt); err != nil {
		t.Fatalf("failed to create table: %v", err)
	}

	for _, row := range rows {
		insertStmt := fmt.Sprintf(`
INSERT INTO testdb.ZCLOUDRECORDING (Z_PK, ZDATE, ZPATH, ZCUSTOMLABEL, ZDURATION)
VALUES (%d, %f, '%s', '%s', %f)
`, row.Z_PK, row.ZDATE, strings.ReplaceAll(row.ZPATH, "'", "''"), strings.ReplaceAll(row.ZCUSTOMLABEL, "'", "''"), row.ZDURATION)
		if _, err := db.Exec(insertStmt); err != nil {
			t.Fatalf("failed to insert row: %v", err)
		}
	}

	if _, err := db.Exec("DETACH testdb"); err != nil {
		t.Fatalf("failed to detach db: %v", err)
	}

	return dbPath
}

func SetupAudioFiles(t *testing.T, appleDBPath string, rows []AppleDBRow) {
	t.Helper()
	recordingsDir := filepath.Dir(appleDBPath)

	sampleData, err := os.ReadFile(filepath.Join("..", "..", "testdata", "sample.m4a"))
	if err != nil {
		sampleData, err = os.ReadFile(filepath.Join("..", "testdata", "sample.m4a"))
		if err != nil {
			sampleData, err = os.ReadFile(filepath.Join("testdata", "sample.m4a"))
			if err != nil {
				t.Fatalf("failed to read sample.m4a: %v", err)
			}
		}
	}

	for _, row := range rows {
		filePath := filepath.Join(recordingsDir, row.ZPATH)
		if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
			t.Fatalf("failed to mkdir: %v", err)
		}
		if err := os.WriteFile(filePath, sampleData, 0644); err != nil {
			t.Fatalf("failed to write audio file: %v", err)
		}
	}
}

func GetDuckDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatalf("failed to open duckdb: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
