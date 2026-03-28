package main

import (
	"database/sql"
	"log"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const retentionDays = 7

// StatPoint is a single stored data point.
type StatPoint struct {
	RecordedAt   int64   `json:"t"`
	CPUPercent   float64 `json:"cpu"`
	RAMPercent   float64 `json:"ram"`
	Temperature  float64 `json:"temp"`
	NetRxBPS     float64 `json:"rx"`
	NetTxBPS     float64 `json:"tx"`
	DiskReadBPS  float64 `json:"dr"`
	DiskWriteBPS float64 `json:"dw"`
}

// HistoryResponse is what /api/history returns.
type HistoryResponse struct {
	Period string      `json:"period"`
	Points []StatPoint `json:"points"`
}

// Storage wraps the SQLite database.
type Storage struct {
	db *sql.DB
}

func openStorage() (*Storage, error) {
	dir := filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "M4Server")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	dbPath := filepath.Join(dir, "stats.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.Exec("PRAGMA journal_mode=WAL;")
	db.Exec("PRAGMA synchronous=NORMAL;")

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS stats (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			recorded_at  INTEGER NOT NULL,
			cpu_percent  REAL    NOT NULL DEFAULT 0,
			ram_percent  REAL    NOT NULL DEFAULT 0,
			temperature  REAL    NOT NULL DEFAULT 0,
			net_rx_bps   REAL    NOT NULL DEFAULT 0,
			net_tx_bps   REAL    NOT NULL DEFAULT 0,
			disk_rd_bps  REAL    NOT NULL DEFAULT 0,
			disk_wr_bps  REAL    NOT NULL DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS idx_stats_at ON stats(recorded_at);
	`); err != nil {
		db.Close()
		return nil, err
	}

	s := &Storage{db: db}
	go s.runCollector()
	go s.runCleaner()
	return s, nil
}

func (s *Storage) Close() {
	if s != nil && s.db != nil {
		s.db.Close()
	}
}

// runCollector collects one stat point per minute, continuously.
func (s *Storage) runCollector() {
	startTempPoller()
	rt := newRateTracker()
	// Prime the rate tracker
	collectStats(rt, "")

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		st := collectStats(rt, "")
		var temp float64
		if st.Temperature != nil {
			temp = *st.Temperature
		}
		if _, err := s.db.Exec(
			`INSERT INTO stats
			 (recorded_at, cpu_percent, ram_percent, temperature, net_rx_bps, net_tx_bps, disk_rd_bps, disk_wr_bps)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			time.Now().Unix(),
			st.CPUPercent,
			st.RAMPercent,
			temp,
			st.NetRxBPS,
			st.NetTxBPS,
			st.DiskReadBPS,
			st.DiskWriteBPS,
		); err != nil {
			log.Printf("storage: insert error: %v", err)
		}
	}
}

// runCleaner removes rows older than retentionDays, once per day.
func (s *Storage) runCleaner() {
	clean := func() {
		cutoff := time.Now().AddDate(0, 0, -retentionDays).Unix()
		res, err := s.db.Exec(`DELETE FROM stats WHERE recorded_at < ?`, cutoff)
		if err != nil {
			log.Printf("storage: clean error: %v", err)
			return
		}
		if n, _ := res.RowsAffected(); n > 0 {
			log.Printf("storage: removed %d rows older than %d days", n, retentionDays)
		}
	}
	clean()
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		clean()
	}
}

// QueryHistory returns aggregated stat points for the given period.
// Supported: "1h" (1-min buckets), "6h" (5-min), "24h" (15-min), "7d" (1-hour).
func (s *Storage) QueryHistory(period string) HistoryResponse {
	now := time.Now().Unix()
	var since, bucketSecs int64

	switch period {
	case "1h":
		since, bucketSecs = now-3600, 60
	case "6h":
		since, bucketSecs = now-6*3600, 5*60
	case "24h":
		since, bucketSecs = now-24*3600, 15*60
	default:
		period = "7d"
		since, bucketSecs = now-7*24*3600, 3600
	}

	rows, err := s.db.Query(`
		SELECT
			(recorded_at / ?) * ? AS bucket,
			AVG(cpu_percent),
			AVG(ram_percent),
			AVG(temperature),
			AVG(net_rx_bps),
			AVG(net_tx_bps),
			AVG(disk_rd_bps),
			AVG(disk_wr_bps)
		FROM stats
		WHERE recorded_at >= ?
		GROUP BY bucket
		ORDER BY bucket ASC
	`, bucketSecs, bucketSecs, since)
	if err != nil {
		log.Printf("storage: query error: %v", err)
		return HistoryResponse{Period: period, Points: []StatPoint{}}
	}
	defer rows.Close()

	points := make([]StatPoint, 0)
	for rows.Next() {
		var p StatPoint
		if err := rows.Scan(
			&p.RecordedAt,
			&p.CPUPercent,
			&p.RAMPercent,
			&p.Temperature,
			&p.NetRxBPS,
			&p.NetTxBPS,
			&p.DiskReadBPS,
			&p.DiskWriteBPS,
		); err != nil {
			continue
		}
		points = append(points, p)
	}
	return HistoryResponse{Period: period, Points: points}
}
