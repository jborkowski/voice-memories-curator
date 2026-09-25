package detect

import (
	"database/sql"
	"encoding/binary"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/jborkowski/vmc/internal/config"
	"github.com/jborkowski/vmc/internal/testutil"
)

const testDirGlob = "????-??-??-??-??/DJI_Audio_*"

// writeWAV writes a 16-bit PCM WAV; dataSizeOverride >= 0 replaces the
// header's data size to mimic unfinalized recordings.
func writeWAV(t *testing.T, path string, sampleRate, channels int, seconds float64, dataSizeOverride int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	blockAlign := channels * 2
	byteRate := sampleRate * blockAlign
	dataSize := int(float64(byteRate) * seconds)
	headerSize := uint32(dataSize)
	if dataSizeOverride >= 0 {
		headerSize = uint32(dataSizeOverride)
	}

	b := make([]byte, 0, 44+dataSize)
	le := binary.LittleEndian
	b = append(b, "RIFF"...)
	b = le.AppendUint32(b, uint32(36+dataSize))
	b = append(b, "WAVE"...)
	b = append(b, "LIST"...)
	b = le.AppendUint32(b, 3)
	b = append(b, 'a', 'b', 'c', 0)
	b = append(b, "fmt "...)
	b = le.AppendUint32(b, 16)
	b = le.AppendUint16(b, 1)
	b = le.AppendUint16(b, uint16(channels))
	b = le.AppendUint32(b, uint32(sampleRate))
	b = le.AppendUint32(b, uint32(byteRate))
	b = le.AppendUint16(b, uint16(blockAlign))
	b = le.AppendUint16(b, 16)
	b = append(b, "data"...)
	b = le.AppendUint32(b, headerSize)
	b = append(b, make([]byte, dataSize)...)
	if err := os.WriteFile(path, b, 0644); err != nil {
		t.Fatal(err)
	}
}

func djiConfig(root string) config.DJI {
	return config.DJI{
		Enabled:       true,
		Root:          root,
		DirGlob:       testDirGlob,
		DeviceLabel:   "DJI Mic",
		MinAgeSeconds: 0,
		PullLock:      filepath.Join(root, ".pull.lock"),
	}
}

func localCreatedAt(t *testing.T, stamp string) string {
	t.Helper()
	tm, err := time.ParseInLocation("20060102150405", stamp, time.Local)
	if err != nil {
		t.Fatal(err)
	}
	return tm.UTC().Format(createdAtLayout)
}

func TestWavDuration(t *testing.T) {
	dir := t.TempDir()

	ok := filepath.Join(dir, "ok.wav")
	writeWAV(t, ok, 48000, 2, 1.5, -1)
	if d, err := wavDuration(ok); err != nil || math.Abs(d-1.5) > 1e-9 {
		t.Fatalf("wavDuration = %v, %v; want 1.5", d, err)
	}

	unfinalized := filepath.Join(dir, "zero.wav")
	writeWAV(t, unfinalized, 16000, 1, 2, 0)
	if d, err := wavDuration(unfinalized); err != nil || math.Abs(d-2) > 1e-9 {
		t.Fatalf("unfinalized wavDuration = %v, %v; want 2", d, err)
	}

	notWav := filepath.Join(dir, "bad.wav")
	os.WriteFile(notWav, []byte("definitely not a wave file"), 0644)
	if _, err := wavDuration(notWav); err == nil {
		t.Fatal("expected error for non-WAV file")
	}
}

func TestParseDJIFilename(t *testing.T) {
	got, ok := parseDJIFilename("DJI_07_20260925_175801.WAV")
	if !ok {
		t.Fatal("expected DJI filename to parse")
	}
	if want := localCreatedAt(t, "20260925175801"); got.UTC().Format(createdAtLayout) != want {
		t.Fatalf("created_at = %s, want %s", got.UTC().Format(createdAtLayout), want)
	}
	for _, bad := range []string{"DJI_07_2026092_175801.WAV", "REC_07_20260925_175801.WAV", "DJI_07_20260925_175801.mp3"} {
		if _, ok := parseDJIFilename(bad); ok {
			t.Errorf("%q should not parse", bad)
		}
	}
}

func TestScanDJIFilters(t *testing.T) {
	root := t.TempDir()
	good := filepath.Join(root, "2026-09-25-17-58", "DJI_Audio_001")
	writeWAV(t, filepath.Join(good, "DJI_02_20260925_180000.WAV"), 16000, 1, 1, -1)
	writeWAV(t, filepath.Join(good, "DJI_01_20260925_175801.WAV"), 16000, 1, 2, -1)
	writeWAV(t, filepath.Join(good, ".DJI_03_20260925_180100.WAV.a1B2c3"), 16000, 1, 1, -1)
	writeWAV(t, filepath.Join(good, ".DJI_04_20260925_180200.WAV.icloud"), 16000, 1, 1, -1)
	writeWAV(t, filepath.Join(good, "notes.wav"), 16000, 1, 1, -1)
	writeWAV(t, filepath.Join(root, "misc", "DJI_Audio_001", "DJI_05_20260925_180300.WAV"), 16000, 1, 1, -1)

	recs, err := scanDJI(djiConfig(root), time.Now())
	if err != nil {
		t.Fatalf("scanDJI: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("expected 2 recordings, got %+v", recs)
	}
	if recs[0].CreatedAt != localCreatedAt(t, "20260925175801") || recs[0].Duration != 2 {
		t.Errorf("first recording should be the oldest: %+v", recs[0])
	}
	if recs[0].Folder != "2026-09-25-17-58/DJI_Audio_001" {
		t.Errorf("folder = %q", recs[0].Folder)
	}

	cfg := djiConfig(root)
	cfg.MinAgeSeconds = 3600
	if recs, err := scanDJI(cfg, time.Now()); err != nil || len(recs) != 0 {
		t.Errorf("unsettled files should be skipped, got %d recs, err %v", len(recs), err)
	}
}

func TestScanDJIPullLock(t *testing.T) {
	cfg := djiConfig(t.TempDir())
	if err := os.Mkdir(cfg.PullLock, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := scanDJI(cfg, time.Now()); !errors.Is(err, errDJILocked) {
		t.Fatalf("expected errDJILocked, got %v", err)
	}
}

func TestAssignDJIIDsSlotOrdering(t *testing.T) {
	existingDJI, _ := EncodeDJI(7, 3)
	known := []int64{5, 7, EncodeApple(6), existingDJI}
	recs := assignDJIIDs(make([]djiRecording, 2), known, 7)
	want0, _ := EncodeDJI(7, 4)
	want1, _ := EncodeDJI(7, 5)
	if len(recs) != 2 || recs[0].ID != want0 || recs[1].ID != want1 {
		t.Fatalf("got %+v, want ids %d, %d", recs, want0, want1)
	}
	if !(7 < EncodeApple(7) && EncodeApple(7) < recs[0].ID && recs[1].ID < EncodeApple(8)) {
		t.Fatal("expected legacy < Apple slot < DJI in slot < next Apple")
	}
}

type shardRow struct {
	ID        int64
	Title     sql.NullString
	CreatedAt string
	Duration  float64
	Device    sql.NullString
	Folder    sql.NullString
	AudioPath string
}

func readShardRows(t *testing.T, db *sql.DB, shardDir string) []shardRow {
	t.Helper()
	pattern := filepath.Join(shardDir, "shard_*.parquet")
	if m, _ := filepath.Glob(pattern); len(m) == 0 {
		return nil
	}
	rows, err := db.Query("SELECT recording_id, title, created_at, duration_seconds, device, folder, audio_path FROM '" + pattern + "' ORDER BY recording_id")
	if err != nil {
		t.Fatalf("read shards: %v", err)
	}
	defer rows.Close()
	var out []shardRow
	for rows.Next() {
		var r shardRow
		if err := rows.Scan(&r.ID, &r.Title, &r.CreatedAt, &r.Duration, &r.Device, &r.Folder, &r.AudioPath); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func setupDJIDetect(t *testing.T) (*config.Config, string) {
	t.Helper()
	testutil.SuppressLogs()

	root := t.TempDir()
	dir := filepath.Join(root, "2026-09-25-17-58", "DJI_Audio_001")
	writeWAV(t, filepath.Join(dir, "DJI_01_20260925_175801.WAV"), 16000, 1, 2, -1)
	writeWAV(t, filepath.Join(dir, "DJI_02_20260925_180000.WAV"), 16000, 1, 1, -1)
	// Same recording pulled twice into another folder must not duplicate.
	writeWAV(t, filepath.Join(root, "2026-09-25-18-30", "DJI_Audio_001", "DJI_02_20260925_180000.WAV"), 16000, 1, 1, -1)

	dbPath := testutil.CreateAppleDB(t, []testutil.AppleDBRow{
		{Z_PK: 1, ZDATE: 1000, ZPATH: "2023/1.m4a", ZCUSTOMLABEL: "Memo 1", ZDURATION: 10.5},
		{Z_PK: 2, ZDATE: 2000, ZPATH: "2023/2.m4a", ZCUSTOMLABEL: "Memo 2", ZDURATION: 5.0},
	})
	cfg := testutil.SetupConfig(t, "", dbPath, t.TempDir())
	cfg.HFToken = ""
	cfg.DJI = djiConfig(root)
	return cfg, root
}

func TestDetectDJIDisabled(t *testing.T) {
	cfg, _ := setupDJIDetect(t)
	cfg.DJI.Enabled = false
	db := testutil.GetDuckDB(t)

	if err := Run(db, cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}
	rows := readShardRows(t, db, cfg.ShardDir)
	if len(rows) != 2 || rows[0].ID != 1 || rows[1].ID != 2 {
		t.Fatalf("expected legacy Apple ids 1, 2 only, got %+v", rows)
	}
}

func TestDetectDJIEnabled(t *testing.T) {
	cfg, root := setupDJIDetect(t)
	db := testutil.GetDuckDB(t)

	if err := Run(db, cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}
	rows := readShardRows(t, db, cfg.ShardDir)
	dji1, _ := EncodeDJI(2, 1)
	dji2, _ := EncodeDJI(2, 2)
	wantIDs := []int64{EncodeApple(1), EncodeApple(2), dji1, dji2}
	if len(rows) != len(wantIDs) {
		t.Fatalf("expected %d rows, got %+v", len(wantIDs), rows)
	}
	for i, id := range wantIDs {
		if rows[i].ID != id {
			t.Errorf("row %d id = %d, want %d", i, rows[i].ID, id)
		}
	}

	d := rows[2]
	if d.Device.String != "DJI Mic" || d.Title.Valid || d.Duration != 2 ||
		d.CreatedAt != localCreatedAt(t, "20260925175801") ||
		d.Folder.String != "2026-09-25-17-58/DJI_Audio_001" ||
		d.AudioPath != filepath.Join(root, "2026-09-25-17-58", "DJI_Audio_001", "DJI_01_20260925_175801.WAV") {
		t.Errorf("unexpected DJI row: %+v", d)
	}

	// Second tick: everything is in local shards, nothing new.
	if err := Run(db, cfg); err != nil {
		t.Fatalf("Run 2: %v", err)
	}
	if again := readShardRows(t, db, cfg.ShardDir); len(again) != len(rows) {
		t.Fatalf("second run should add nothing, got %d rows", len(again))
	}
}

func TestDetectDJIAppleFailureDoesNotBlock(t *testing.T) {
	cfg, _ := setupDJIDetect(t)
	cfg.AppleDBPath = filepath.Join(t.TempDir(), "missing.db")
	db := testutil.GetDuckDB(t)

	if err := Run(db, cfg); err == nil {
		t.Fatal("expected Apple error to be reported")
	}
	rows := readShardRows(t, db, cfg.ShardDir)
	if len(rows) != 2 || rows[0].Device.String != "DJI Mic" || rows[1].Device.String != "DJI Mic" {
		t.Fatalf("expected 2 DJI rows despite Apple failure, got %+v", rows)
	}
}

func TestDetectDJIRemote(t *testing.T) {
	cfg, _ := setupDJIDetect(t)
	db := testutil.GetDuckDB(t)

	// Hub already has legacy Apple 1 and the 18:00 DJI recording in slot 1.
	published, _ := EncodeDJI(1, 1)
	parquetPath := filepath.Join(t.TempDir(), "shard_0001.parquet")
	if _, err := db.Exec(`COPY (
		SELECT CAST(1 AS BIGINT) AS recording_id, 'iPhone' AS device, '2001-01-01T00:16:40Z' AS created_at, CAST(10.5 AS DOUBLE) AS duration_seconds
		UNION ALL
		SELECT CAST(` + strconv.FormatInt(published, 10) + ` AS BIGINT), 'DJI Mic', '` + localCreatedAt(t, "20260925180000") + `', CAST(1 AS DOUBLE)
	) TO '` + parquetPath + `' (FORMAT PARQUET)`); err != nil {
		t.Fatal(err)
	}
	parquetBytes, _ := os.ReadFile(parquetPath)

	fail := false
	mux := http.NewServeMux()
	mux.HandleFunc("/api/datasets/test/repo/tree/main/data", func(w http.ResponseWriter, r *http.Request) {
		if fail {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Write([]byte(`[{"type":"file","path":"data/shard_0001.parquet"}]`))
	})
	mux.HandleFunc("/datasets/test/repo/resolve/main/data/shard_0001.parquet", func(w http.ResponseWriter, r *http.Request) {
		w.Write(parquetBytes)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	cfg.HFToken = "tok"
	cfg.HFRepo = "test/repo"
	cfg.HFBaseURL = ts.URL

	t.Run("fails closed", func(t *testing.T) {
		fail = true
		defer func() { fail = false }()
		shardDir := t.TempDir()
		c := *cfg
		c.ShardDir = shardDir
		if err := Run(db, &c); err != nil {
			t.Fatalf("Run: %v", err)
		}
		for _, r := range readShardRows(t, db, shardDir) {
			if r.Device.String == "DJI Mic" {
				t.Fatalf("DJI rows must be skipped when remote listing fails: %+v", r)
			}
		}
	})

	t.Run("dedupes and allocates after remote", func(t *testing.T) {
		if err := Run(db, cfg); err != nil {
			t.Fatalf("Run: %v", err)
		}
		rows := readShardRows(t, db, cfg.ShardDir)
		dji, _ := EncodeDJI(2, 1)
		wantIDs := []int64{EncodeApple(2), dji}
		if len(rows) != len(wantIDs) {
			t.Fatalf("expected Apple 2 + one new DJI row, got %+v", rows)
		}
		for i, id := range wantIDs {
			if rows[i].ID != id {
				t.Errorf("row %d id = %d, want %d", i, rows[i].ID, id)
			}
		}
		if rows[1].CreatedAt != localCreatedAt(t, "20260925175801") {
			t.Errorf("wrong DJI recording kept: %+v", rows[1])
		}
	})
}
