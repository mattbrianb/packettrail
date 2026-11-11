// Package store persists traffic history in a local SQLite file.
package store

import (
	"database/sql"
	"fmt"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"packettrail/internal/capture"
	"packettrail/internal/core"
)

const schema = `
CREATE TABLE IF NOT EXISTS apps(
  id INTEGER PRIMARY KEY,
  key TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  path TEXT NOT NULL DEFAULT '',
  first_seen INTEGER NOT NULL,
  last_seen INTEGER NOT NULL
);
-- Traffic per app and remote endpoint, in 5-minute buckets (rolled up to
-- hourly buckets after two days). Bytes are payload bytes.
CREATE TABLE IF NOT EXISTS flows(
  bucket INTEGER NOT NULL,
  app_id INTEGER NOT NULL,
  proto INTEGER NOT NULL,      -- 6 = TCP, 17 = UDP
  ip TEXT NOT NULL,            -- remote address
  port INTEGER NOT NULL,       -- remote port (dir=1) or local port (dir=2)
  dir INTEGER NOT NULL,        -- 1 = outbound, 2 = inbound
  bytes_in INTEGER NOT NULL,
  bytes_out INTEGER NOT NULL,
  first_seen INTEGER NOT NULL,
  last_seen INTEGER NOT NULL,
  host TEXT NOT NULL DEFAULT '', -- DNS name the app looked up
  PRIMARY KEY(bucket, app_id, proto, ip, port, dir)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS flows_ip ON flows(ip);
-- Per-app traffic per minute (rolled up to hourly after two days), for charts.
CREATE TABLE IF NOT EXISTS app_series(
  minute INTEGER NOT NULL,
  app_id INTEGER NOT NULL,
  bytes_in INTEGER NOT NULL,
  bytes_out INTEGER NOT NULL,
  PRIMARY KEY(minute, app_id)
) WITHOUT ROWID;
-- Cached lookups: reverse DNS and the last name seen in DNS answers.
CREATE TABLE IF NOT EXISTS ipinfo(
  ip TEXT PRIMARY KEY,
  ptr TEXT NOT NULL DEFAULT '',
  ptr_at INTEGER NOT NULL DEFAULT 0,
  host TEXT NOT NULL DEFAULT '',
  host_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS meta(k TEXT PRIMARY KEY, v TEXT NOT NULL);
`

// Tables added after the first release (created by migrate).
const extraSchema = `
-- Latency monitors: pinned targets probed in the background.
CREATE TABLE IF NOT EXISTS monitors(
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL,
  target TEXT NOT NULL,          -- IP address or hostname
  method TEXT NOT NULL,          -- icmp | tcp
  port INTEGER NOT NULL DEFAULT 0,
  interval_s INTEGER NOT NULL DEFAULT 2,
  enabled INTEGER NOT NULL DEFAULT 1,
  created INTEGER NOT NULL
);
-- Monitor results per minute: probes sent/answered and RTT min/max/sum/sum of squares (ms).
CREATE TABLE IF NOT EXISTS monitor_series(
  monitor_id INTEGER NOT NULL,
  minute INTEGER NOT NULL,
  sent INTEGER NOT NULL,
  recv INTEGER NOT NULL,
  rtt_min REAL NOT NULL,
  rtt_max REAL NOT NULL,
  rtt_sum REAL NOT NULL,
  rtt_sq REAL NOT NULL,
  PRIMARY KEY(monitor_id, minute)
) WITHOUT ROWID;
-- Cached PeeringDB records (JSON), refreshed weekly.
CREATE TABLE IF NOT EXISTS peeringdb(
  asn INTEGER PRIMARY KEY,
  data TEXT NOT NULL,
  fetched INTEGER NOT NULL
);
`

const flowUpsert = `
ON CONFLICT(bucket, app_id, proto, ip, port, dir) DO UPDATE SET
  bytes_in = bytes_in + excluded.bytes_in,
  bytes_out = bytes_out + excluded.bytes_out,
  first_seen = min(first_seen, excluded.first_seen),
  last_seen = max(last_seen, excluded.last_seen),
  host = CASE WHEN excluded.host <> '' THEN excluded.host ELSE host END,
  retrans = retrans + excluded.retrans,
  retrans_pkts = retrans_pkts + excluded.retrans_pkts,
  rtt_sum = rtt_sum + excluded.rtt_sum,
  rtt_n = rtt_n + excluded.rtt_n,
  loss_in = loss_in + excluded.loss_in,
  segs_in = segs_in + excluded.segs_in`

// Columns added after the first release; Open adds them to older databases.
var flowAddedColumns = [][2]string{
	{"retrans", "INTEGER NOT NULL DEFAULT 0"},      // bytes retransmitted
	{"retrans_pkts", "INTEGER NOT NULL DEFAULT 0"}, // segments retransmitted
	{"rtt_sum", "REAL NOT NULL DEFAULT 0"},         // sum of RTT samples (ms)
	{"rtt_n", "INTEGER NOT NULL DEFAULT 0"},        // number of RTT samples
	{"loss_in", "INTEGER NOT NULL DEFAULT 0"},      // download-side loss events (duplicate-ACK episodes)
	{"segs_in", "INTEGER NOT NULL DEFAULT 0"},      // segments received while sampling
}

func migrate(db *sql.DB) error {
	have := map[string]bool{}
	rows, err := db.Query(`PRAGMA table_info(flows)`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err == nil {
			have[name] = true
		}
	}
	rows.Close()
	for _, c := range flowAddedColumns {
		if !have[c[0]] {
			if _, err := db.Exec(`ALTER TABLE flows ADD COLUMN ` + c[0] + ` ` + c[1]); err != nil {
				return err
			}
		}
	}
	_, err = db.Exec(extraSchema)
	return err
}

const seriesUpsert = `
ON CONFLICT(minute, app_id) DO UPDATE SET
  bytes_in = bytes_in + excluded.bytes_in,
  bytes_out = bytes_out + excluded.bytes_out`

type Store struct {
	db   *sql.DB
	mu   sync.Mutex
	ids  map[string]int64
	keys map[int64]core.AppMeta
	Path string
}

func Open(path string) (*Store, error) {
	dsn := "file:" + strings.ReplaceAll(path, `\`, "/") +
		"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(off)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("upgrade schema: %w", err)
	}
	s := &Store{db: db, ids: map[string]int64{}, keys: map[int64]core.AppMeta{}, Path: path}
	rows, err := db.Query(`SELECT id, key, name, path, first_seen, last_seen FROM apps`)
	if err != nil {
		db.Close()
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var m core.AppMeta
		if err := rows.Scan(&id, &m.Key, &m.Name, &m.Path, &m.First, &m.Last); err == nil {
			s.ids[m.Key] = id
			s.keys[id] = m
		}
	}
	return s, rows.Err()
}

func (s *Store) Close() error { return s.db.Close() }

// appID returns the id for key, inserting a row if needed. s.mu must be held.
func (s *Store) appID(tx *sql.Tx, m core.AppMeta) (int64, error) {
	if id, ok := s.ids[m.Key]; ok {
		return id, nil
	}
	if m.Name == "" {
		m.Name = m.Key
	}
	now := time.Now().Unix()
	if m.First == 0 {
		m.First = now
	}
	if m.Last == 0 {
		m.Last = m.First
	}
	res, err := tx.Exec(`INSERT INTO apps(key, name, path, first_seen, last_seen) VALUES(?,?,?,?,?)
		ON CONFLICT(key) DO UPDATE SET name = excluded.name`, m.Key, m.Name, m.Path, m.First, m.Last)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil || id == 0 {
		if err := tx.QueryRow(`SELECT id FROM apps WHERE key = ?`, m.Key).Scan(&id); err != nil {
			return 0, err
		}
	}
	s.ids[m.Key] = id
	s.keys[id] = m
	return id, nil
}

// Write persists one flush worth of pending engine data in a transaction.
func (s *Store) Write(flows []core.PendFlow, series []core.PendSeries, apps []core.AppMeta, dns []core.DNSRow) error {
	if len(flows)+len(series)+len(apps)+len(dns) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, a := range apps {
		id, err := s.appID(tx, a)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE apps SET name = ?, path = CASE WHEN ? <> '' THEN ? ELSE path END,
			last_seen = max(last_seen, ?) WHERE id = ?`, a.Name, a.Path, a.Path, a.Last, id); err != nil {
			return err
		}
		m := s.keys[id]
		m.Name = a.Name
		if a.Path != "" {
			m.Path = a.Path
		}
		if a.Last > m.Last {
			m.Last = a.Last
		}
		s.keys[id] = m
	}
	fst, err := tx.Prepare(`INSERT INTO flows(bucket, app_id, proto, ip, port, dir, bytes_in, bytes_out, first_seen, last_seen, host,
		retrans, retrans_pkts, rtt_sum, rtt_n, loss_in, segs_in)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)` + flowUpsert)
	if err != nil {
		return err
	}
	defer fst.Close()
	for _, f := range flows {
		id, err := s.appID(tx, core.AppMeta{Key: f.App, First: f.First, Last: f.Last})
		if err != nil {
			return err
		}
		if _, err := fst.Exec(f.Bucket, id, int(f.Proto), f.IP.String(), int(f.Port), int(f.Dir),
			int64(f.In), int64(f.Out), f.First, f.Last, f.Host,
			int64(f.Retrans), int64(f.RetransPkts), f.RTTSum, int64(f.RTTN), int64(f.LossIn), int64(f.SegsIn)); err != nil {
			return err
		}
	}
	sst, err := tx.Prepare(`INSERT INTO app_series(minute, app_id, bytes_in, bytes_out) VALUES(?,?,?,?)` + seriesUpsert)
	if err != nil {
		return err
	}
	defer sst.Close()
	for _, p := range series {
		id, err := s.appID(tx, core.AppMeta{Key: p.App})
		if err != nil {
			return err
		}
		if _, err := sst.Exec(p.Minute, id, int64(p.In), int64(p.Out)); err != nil {
			return err
		}
	}
	for _, d := range dns {
		if _, err := tx.Exec(`INSERT INTO ipinfo(ip, host, host_at) VALUES(?,?,?)
			ON CONFLICT(ip) DO UPDATE SET host = excluded.host, host_at = excluded.host_at`, d.IP.String(), d.Name, d.At); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Apps returns metadata for every app ever seen.
func (s *Store) Apps() map[string]core.AppMeta {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := make(map[string]core.AppMeta, len(s.keys))
	for _, a := range s.keys {
		m[a.Key] = a
	}
	return m
}

// FlowRows returns traffic per app and endpoint with activity since `since`
// (unix seconds; 0 = everything).
func (s *Store) FlowRows(since int64) ([]core.FlowRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT app_id, proto, ip, port, dir, SUM(bytes_in), SUM(bytes_out), MIN(first_seen), MAX(last_seen), MAX(host),
		SUM(retrans), SUM(retrans_pkts), SUM(rtt_sum), SUM(rtt_n), SUM(loss_in), SUM(segs_in)
		FROM flows WHERE bucket >= ? AND last_seen >= ? GROUP BY app_id, proto, ip, port, dir`, since-3600, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.FlowRow
	for rows.Next() {
		var id, in, outB, rtx, rtxp, rttn, lossIn, segsIn int64
		var proto, port, dir int
		var ip string
		var r core.FlowRow
		if err := rows.Scan(&id, &proto, &ip, &port, &dir, &in, &outB, &r.First, &r.Last, &r.Host, &rtx, &rtxp, &r.RTTSum, &rttn, &lossIn, &segsIn); err != nil {
			return nil, err
		}
		addr, err := netip.ParseAddr(ip)
		if err != nil {
			continue
		}
		r.App = s.keys[id].Key
		r.Proto, r.IP, r.Port, r.Dir = capture.Proto(proto), addr, uint16(port), capture.Dir(dir)
		r.In, r.Out = uint64(in), uint64(outB)
		r.Retrans, r.RetransPkts, r.RTTN = uint64(rtx), uint64(rtxp), uint64(rttn)
		r.LossIn, r.SegsIn = uint64(lossIn), uint64(segsIn)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Series returns bytes per `step` seconds since `since`, for one app or (key
// empty) all apps.
func (s *Store) Series(key string, since, step int64) ([]core.Point, error) {
	s.mu.Lock()
	id, known := s.ids[key]
	s.mu.Unlock()
	if key != "" && !known {
		return nil, nil
	}
	q := `SELECT (minute / ?) * ?, SUM(bytes_in), SUM(bytes_out) FROM app_series WHERE minute >= ?`
	args := []any{step, step, since}
	if key != "" {
		q += ` AND app_id = ?`
		args = append(args, id)
	}
	q += ` GROUP BY 1 ORDER BY 1`
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pts []core.Point
	for rows.Next() {
		var t, in, out int64
		if err := rows.Scan(&t, &in, &out); err != nil {
			return nil, err
		}
		pts = append(pts, core.Point{T: t, In: float64(in), Out: float64(out)})
	}
	return pts, rows.Err()
}

// LoadDNS returns persisted IP -> hostname mappings newer than since.
func (s *Store) LoadDNS(since int64) []core.DNSRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT ip, host, host_at FROM ipinfo WHERE host <> '' AND host_at >= ?`, since)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []core.DNSRow
	for rows.Next() {
		var ip, host string
		var at int64
		if rows.Scan(&ip, &host, &at) == nil {
			if a, err := netip.ParseAddr(ip); err == nil {
				out = append(out, core.DNSRow{IP: a, Name: host, At: at})
			}
		}
	}
	return out
}

// PTR is a cached reverse DNS result.
type PTR struct {
	Name string
	At   int64
}

func (s *Store) LoadPTR(since int64) map[netip.Addr]PTR {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := map[netip.Addr]PTR{}
	rows, err := s.db.Query(`SELECT ip, ptr, ptr_at FROM ipinfo WHERE ptr_at >= ?`, since)
	if err != nil {
		return m
	}
	defer rows.Close()
	for rows.Next() {
		var ip, ptr string
		var at int64
		if rows.Scan(&ip, &ptr, &at) == nil {
			if a, err := netip.ParseAddr(ip); err == nil {
				m[a] = PTR{ptr, at}
			}
		}
	}
	return m
}

func (s *Store) SavePTR(m map[netip.Addr]PTR) error {
	if len(m) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for ip, p := range m {
		if _, err := tx.Exec(`INSERT INTO ipinfo(ip, ptr, ptr_at) VALUES(?,?,?)
			ON CONFLICT(ip) DO UPDATE SET ptr = excluded.ptr, ptr_at = excluded.ptr_at`, ip.String(), p.Name, p.At); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Maintain rolls fine-grained rows older than two days up into hourly rows
// and deletes data older than the retention period.
func (s *Store) Maintain(retentionDays int) error {
	now := time.Now().Unix()
	cut := now - 2*86400
	cut -= cut % 3600
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmts := []string{
		`INSERT INTO flows(bucket, app_id, proto, ip, port, dir, bytes_in, bytes_out, first_seen, last_seen, host, retrans, retrans_pkts, rtt_sum, rtt_n, loss_in, segs_in)
		 SELECT bucket - bucket % 3600, app_id, proto, ip, port, dir, SUM(bytes_in), SUM(bytes_out), MIN(first_seen), MAX(last_seen), MAX(host),
		        SUM(retrans), SUM(retrans_pkts), SUM(rtt_sum), SUM(rtt_n), SUM(loss_in), SUM(segs_in)
		 FROM flows WHERE bucket < ?1 AND bucket % 3600 <> 0 GROUP BY 1, 2, 3, 4, 5, 6` + flowUpsert,
		`DELETE FROM flows WHERE bucket < ?1 AND bucket % 3600 <> 0`,
		`INSERT INTO app_series(minute, app_id, bytes_in, bytes_out)
		 SELECT minute - minute % 3600, app_id, SUM(bytes_in), SUM(bytes_out)
		 FROM app_series WHERE minute < ?1 AND minute % 3600 <> 0 GROUP BY 1, 2` + seriesUpsert,
		`DELETE FROM app_series WHERE minute < ?1 AND minute % 3600 <> 0`,
	}
	for _, q := range stmts {
		if _, err := tx.Exec(q, cut); err != nil {
			return fmt.Errorf("rollup: %w", err)
		}
	}
	if retentionDays > 0 {
		old := now - int64(retentionDays)*86400
		for _, q := range []string{`DELETE FROM flows WHERE bucket < ?`, `DELETE FROM app_series WHERE minute < ?`,
			`DELETE FROM monitor_series WHERE minute < ?`,
			`DELETE FROM ipinfo WHERE max(ptr_at, host_at) < ?`} {
			if _, err := tx.Exec(q, old); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// Size returns the database size on disk in bytes (main file + WAL).
func (s *Store) Size() int64 {
	var n int64
	for _, p := range []string{s.Path, s.Path + "-wal"} {
		if st, err := os.Stat(p); err == nil {
			n += st.Size()
		}
	}
	return n
}
