package store

import (
	"database/sql"
	"math"
)

// Monitor is a pinned latency target.
type Monitor struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Target   string `json:"target"`
	Method   string `json:"method"` // icmp | tcp
	Port     int    `json:"port"`
	Interval int    `json:"interval"` // seconds between probes
	Enabled  bool   `json:"enabled"`
	Created  int64  `json:"created"`
}

// MonitorMinute is one minute of probe results.
type MonitorMinute struct {
	MonitorID         int64
	Minute            int64
	Sent, Recv        int
	Min, Max, Sum, Sq float64
}

// MonitorPoint is an aggregated chart point.
type MonitorPoint struct {
	T    int64   `json:"t"`
	Sent int     `json:"sent"`
	Recv int     `json:"recv"`
	Min  float64 `json:"min"`
	Avg  float64 `json:"avg"`
	Max  float64 `json:"max"`
	SD   float64 `json:"sd"`
}

func (s *Store) Monitors() ([]Monitor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT id, name, target, method, port, interval_s, enabled, created FROM monitors ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Monitor
	for rows.Next() {
		var m Monitor
		var en int
		if err := rows.Scan(&m.ID, &m.Name, &m.Target, &m.Method, &m.Port, &m.Interval, &en, &m.Created); err != nil {
			return nil, err
		}
		m.Enabled = en != 0
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) AddMonitor(m Monitor) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`INSERT INTO monitors(name, target, method, port, interval_s, enabled, created) VALUES(?,?,?,?,?,?,?)`,
		m.Name, m.Target, m.Method, m.Port, m.Interval, boolInt(m.Enabled), m.Created)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) DeleteMonitor(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM monitor_series WHERE monitor_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM monitors WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetMonitorEnabled(id int64, on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE monitors SET enabled = ? WHERE id = ?`, boolInt(on), id)
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// WriteMonitorMinutes upserts minute aggregates (a minute may be written
// twice if a monitor was paused and resumed within it).
func (s *Store) WriteMonitorMinutes(rows []MonitorMinute) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	st, err := tx.Prepare(`INSERT INTO monitor_series(monitor_id, minute, sent, recv, rtt_min, rtt_max, rtt_sum, rtt_sq) VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(monitor_id, minute) DO UPDATE SET sent = sent + excluded.sent, recv = recv + excluded.recv,
		  rtt_min = CASE WHEN recv = 0 THEN excluded.rtt_min WHEN excluded.recv = 0 THEN rtt_min ELSE min(rtt_min, excluded.rtt_min) END,
		  rtt_max = max(rtt_max, excluded.rtt_max), rtt_sum = rtt_sum + excluded.rtt_sum, rtt_sq = rtt_sq + excluded.rtt_sq`)
	if err != nil {
		return err
	}
	defer st.Close()
	for _, r := range rows {
		if _, err := st.Exec(r.MonitorID, r.Minute, r.Sent, r.Recv, r.Min, r.Max, r.Sum, r.Sq); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// MonitorSeries aggregates a monitor's minutes into step-second buckets.
func (s *Store) MonitorSeries(id, since, step int64) ([]MonitorPoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT (minute / ?1) * ?1, SUM(sent), SUM(recv), MIN(CASE WHEN recv > 0 THEN rtt_min END), MAX(rtt_max), SUM(rtt_sum), SUM(rtt_sq)
		FROM monitor_series WHERE monitor_id = ?2 AND minute >= ?3 GROUP BY 1 ORDER BY 1`, step, id, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MonitorPoint{}
	for rows.Next() {
		var p MonitorPoint
		var mn sql.NullFloat64
		var sum, sq float64
		if err := rows.Scan(&p.T, &p.Sent, &p.Recv, &mn, &p.Max, &sum, &sq); err != nil {
			return nil, err
		}
		if p.Recv > 0 {
			p.Min = mn.Float64
			p.Avg = sum / float64(p.Recv)
			if v := sq/float64(p.Recv) - p.Avg*p.Avg; v > 0 {
				p.SD = math.Sqrt(v)
			}
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetMeta returns a stored setting ("" if unset).
func (s *Store) GetMeta(k string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var v string
	s.db.QueryRow(`SELECT v FROM meta WHERE k = ?`, k).Scan(&v)
	return v
}

// SetMeta stores a setting.
func (s *Store) SetMeta(k, v string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO meta(k, v) VALUES(?, ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`, k, v)
	return err
}

// PeeringDB returns a cached PeeringDB record (JSON) and when it was fetched.
func (s *Store) PeeringDB(asn uint32) (string, int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var data string
	var at int64
	if err := s.db.QueryRow(`SELECT data, fetched FROM peeringdb WHERE asn = ?`, asn).Scan(&data, &at); err != nil {
		return "", 0, false
	}
	return data, at, true
}

// SavePeeringDB caches a PeeringDB record.
func (s *Store) SavePeeringDB(asn uint32, data string, at int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO peeringdb(asn, data, fetched) VALUES(?,?,?) ON CONFLICT(asn) DO UPDATE SET data = excluded.data, fetched = excluded.fetched`, asn, data, at)
	return err
}
