package store

import (
	"database/sql"
	"fmt"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"packettrail/internal/capture"
	"packettrail/internal/core"
)

func TestWriteQueryRollupRetention(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().Unix()
	ip := netip.MustParseAddr("1.1.1.1")
	key := core.FlowKey{App: "chrome.exe", Proto: capture.TCP, IP: ip, Port: 443, Dir: capture.DirOut}
	var flows []core.PendFlow
	var series []core.PendSeries
	// 12 five-minute buckets three days ago (one hour), plus one recent bucket,
	// plus one bucket 100 days ago that retention must delete.
	old := now - 3*86400
	old -= old % 3600
	for i := int64(0); i < 12; i++ {
		b := old + i*300
		flows = append(flows, core.PendFlow{Bucket: b, FlowRow: core.FlowRow{FlowKey: key, In: 1000, Out: 10, First: b, Last: b + 299, Host: "one.one.one.one"}})
		series = append(series, core.PendSeries{Minute: b, App: "chrome.exe", In: 1000, Out: 10})
	}
	recent := now - now%300
	flows = append(flows, core.PendFlow{Bucket: recent, FlowRow: core.FlowRow{FlowKey: key, In: 5, Out: 5, First: now, Last: now}})
	ancient := now - 100*86400
	ancient -= ancient % 300
	flows = append(flows, core.PendFlow{Bucket: ancient, FlowRow: core.FlowRow{FlowKey: key, In: 777, Out: 0, First: ancient, Last: ancient}})
	apps := []core.AppMeta{{Key: "chrome.exe", Name: "chrome.exe", Path: `C:\chrome.exe`, First: old, Last: now}}
	if err := st.Write(flows, series, apps, []core.DNSRow{{IP: ip, Name: "one.one.one.one", At: now}}); err != nil {
		t.Fatal(err)
	}
	// Writing the same delta again must accumulate, not replace.
	if err := st.Write(flows[12:13], nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	sum := func(since int64) (uint64, int) {
		rows, err := st.FlowRows(since)
		if err != nil {
			t.Fatal(err)
		}
		var in uint64
		for _, r := range rows {
			in += r.In
		}
		return in, len(rows)
	}
	if in, n := sum(now - 3600); in != 10 || n != 1 {
		t.Fatalf("last hour: in=%d rows=%d, want 10/1", in, n)
	}
	if in, _ := sum(now - 7*86400); in != 12000+10 {
		t.Fatalf("last week before rollup: in=%d", in)
	}
	var before int
	st.db.QueryRow(`SELECT COUNT(*) FROM flows`).Scan(&before)
	if err := st.Maintain(90); err != nil {
		t.Fatal(err)
	}
	var after int
	st.db.QueryRow(`SELECT COUNT(*) FROM flows`).Scan(&after)
	if in, _ := sum(now - 7*86400); in != 12000+10 {
		t.Fatalf("last week after rollup: in=%d (rows %d -> %d)", in, before, after)
	}
	if after != 2 { // one hourly row + one recent row; the ancient row is gone
		t.Fatalf("rows after rollup+retention = %d (before %d), want 2", after, before)
	}
	pts, err := st.Series("chrome.exe", now-7*86400, 3600)
	if err != nil || len(pts) != 1 || pts[0].In != 12000 {
		t.Fatalf("series after rollup: %+v %v", pts, err)
	}
	if d := st.LoadDNS(0); len(d) != 1 || d[0].Name != "one.one.one.one" {
		t.Fatalf("dns rows: %+v", d)
	}
	// Reopen: app metadata survives.
	st.Close()
	st2, err := Open(st.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if a := st2.Apps()["chrome.exe"]; a.Path != `C:\chrome.exe` {
		t.Fatalf("app meta after reopen: %+v", a)
	}
}

func TestMigrateOldSchemaAndQuality(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	// Build a database with the first-release flows table (no quality columns).
	old, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	_, err = old.Exec(`CREATE TABLE flows(bucket INTEGER NOT NULL, app_id INTEGER NOT NULL, proto INTEGER NOT NULL, ip TEXT NOT NULL,
		port INTEGER NOT NULL, dir INTEGER NOT NULL, bytes_in INTEGER NOT NULL, bytes_out INTEGER NOT NULL, first_seen INTEGER NOT NULL,
		last_seen INTEGER NOT NULL, host TEXT NOT NULL DEFAULT '', PRIMARY KEY(bucket, app_id, proto, ip, port, dir)) WITHOUT ROWID;
		CREATE TABLE apps(id INTEGER PRIMARY KEY, key TEXT NOT NULL UNIQUE, name TEXT NOT NULL, path TEXT NOT NULL DEFAULT '', first_seen INTEGER NOT NULL, last_seen INTEGER NOT NULL);
		INSERT INTO apps VALUES(1, 'old.exe', 'old.exe', '', 1, 1);
		INSERT INTO flows VALUES(` + fmt.Sprint(time.Now().Unix()-time.Now().Unix()%300) + `, 1, 6, '9.9.9.9', 443, 1, 100, 50, 1, ` + fmt.Sprint(time.Now().Unix()) + `, '');`)
	if err != nil {
		t.Fatal(err)
	}
	old.Close()
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open old db: %v", err)
	}
	defer st.Close()
	now := time.Now().Unix()
	key := core.FlowKey{App: "old.exe", Proto: capture.TCP, IP: netip.MustParseAddr("9.9.9.9"), Port: 443, Dir: capture.DirOut}
	pf := core.PendFlow{Bucket: now - now%300, FlowRow: core.FlowRow{FlowKey: key, Out: 1000, First: now, Last: now,
		Retrans: 30, RetransPkts: 2, RTTSum: 40, RTTN: 2}}
	if err := st.Write([]core.PendFlow{pf, pf}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	rows, err := st.FlowRows(now - 3600)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows %v err %v", rows, err)
	}
	r := rows[0]
	if r.In != 100 || r.Out != 2050 || r.Retrans != 60 || r.RetransPkts != 4 || r.RTTSum != 80 || r.RTTN != 4 {
		t.Fatalf("migrated/quality row wrong: %+v", r)
	}
	var n int
	st.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('monitors','monitor_series','peeringdb')`).Scan(&n)
	if n != 3 {
		t.Fatalf("new tables missing: %d", n)
	}
}
