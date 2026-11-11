package sysmon

import "testing"

func TestLinuxParsers(t *testing.T) {
	all, cores := parseProcStat("cpu  100 5 50 800 20 3 2 1 0 0\ncpu0 50 2 25 400 10 1 1 0 0 0\ncpu1 50 3 25 400 10 2 1 1 0 0\nintr 123\n")
	if all.user != 100 || all.steal != 1 || len(cores) != 2 || cores[1].irq != 2 {
		t.Fatalf("stat: %+v %+v", all, cores)
	}
	later := cpuLine{user: 150, nice: 5, system: 60, idle: 830, iowait: 30, irq: 3, softirq: 2, steal: 1}
	busy, user, sys, _, wait, _ := cpuPct(later, all)
	// delta: user 50, system 10, idle 30, iowait 10 → total 100, busy 60
	if busy != 60 || user != 50 || sys != 10 || wait != 10 {
		t.Fatalf("cpuPct busy=%v user=%v sys=%v wait=%v", busy, user, sys, wait)
	}

	m := parseKV("MemTotal:       16384000 kB\nMemAvailable:    8192000 kB\nSwapTotal: 0 kB\nHugePages_Total: 0\n")
	if m["MemTotal"] != 16384000*1024 || m["MemAvailable"] != 8192000*1024 || m["HugePages_Total"] != 0 {
		t.Fatalf("meminfo %+v", m)
	}
	if l := parseLoadavg("0.52 0.58 0.59 1/123 4567\n"); len(l) != 3 || l[2] != 0.59 {
		t.Fatalf("loadavg %v", l)
	}

	dev := parseNetDev(`Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 1000      10    0    0    0     0          0         0     1000      10    0    0    0     0       0          0
  eth0: 123456789 1000  7    3    0     0          0         5    987654    900   2    1    0     0       0          0
`)
	if e := dev["eth0"]; e.rxBytes != 123456789 || e.rxErrs != 7 || e.rxDrop != 3 || e.txBytes != 987654 || e.txErrs != 2 || e.txDrop != 1 {
		t.Fatalf("net/dev %+v", e)
	}

	ds := parseDiskstats("   8       0 sda 1000 0 20000 500 2000 0 40000 1500 2 1800 2000 0 0 0 0\n 8 1 sda1 10 0 20 5 20 0 40 15 0 18 20\n")
	if d := ds["sda"]; d.reads != 1000 || d.sectorsRead != 20000 || d.msReading != 500 || d.writes != 2000 || d.sectorsWritten != 40000 || d.msWriting != 1500 || d.inFlight != 2 || d.msIO != 1800 {
		t.Fatalf("diskstats %+v", d)
	}

	sn := parseSnmp(`Ip: Forwarding DefaultTTL
Ip: 1 64
Tcp: RtoAlgorithm RtoMin RtoMax MaxConn ActiveOpens PassiveOpens AttemptFails EstabResets CurrEstab InSegs OutSegs RetransSegs InErrs OutRsts InCsumErrors
Tcp: 1 200 120000 -1 500 20 3 4 12 100000 90000 450 0 30 0
Udp: InDatagrams NoPorts InErrors OutDatagrams RcvbufErrors SndbufErrors InCsumErrors IgnoredMulti MemErrors
Udp: 5000 10 7 4000 7 0 0 0 0
`)
	if sn["Tcp"]["RetransSegs"] != 450 || sn["Tcp"]["CurrEstab"] != 12 || sn["Udp"]["InErrors"] != 7 {
		t.Fatalf("snmp %+v", sn)
	}
	if _, ok := sn["Tcp"]["MaxConn"]; ok {
		t.Fatal("negative MaxConn should be dropped")
	}

	st, ok := parsePidStat("1234 (Web Content (x)) S 1 1234 1234 0 -1 4194560 100 0 0 0 250 50 0 0 20 0 17 0 98765 1000000 2500 18446744073709551615")
	if !ok || st.name != "Web Content (x)" || st.utime != 250 || st.stime != 50 || st.threads != 17 || st.start != 98765 || st.rssPages != 2500 {
		t.Fatalf("pid stat %+v", st)
	}
	if n, ok := parseProcIO("rchar: 100\nwchar: 50\nsyscr: 3\nread_bytes: 0\n"); !ok || n != 150 {
		t.Fatalf("proc io %d %v", n, ok)
	}
}

func TestMacParsers(t *testing.T) {
	p := iostatParser{disks: -1}
	lines := []string{
		"              disk0               disk4       cpu    load average",
		"    KB/t  tps  MB/s     KB/t  tps  MB/s  us sy id   1m   5m   15m",
		"   20.53   23  0.47    12.00    1  0.25   5  3 92  1.92 2.06 2.12",
	}
	var ok bool
	var mbps, us, sy, id float64
	var load []float64
	for _, l := range lines {
		mbps, us, sy, id, load, ok = p.line(l)
	}
	if !ok || mbps != 0.72 || us != 5 || sy != 3 || id != 92 || load[0] != 1.92 {
		t.Fatalf("iostat %v %v %v %v %v %v", ok, mbps, us, sy, id, load)
	}

	page, vm := parseVMStat(`Mach Virtual Memory Statistics: (page size of 16384 bytes)
Pages free:                               12345.
Pages wired down:                        100000.
Pages occupied by compressor:             50000.
Anonymous pages:                         400000.
Pages purgeable:                           1000.
`)
	if page != 16384 || vm["Pages wired down"] != 100000 || vm["Anonymous pages"] != 400000 {
		t.Fatalf("vm_stat %d %+v", page, vm)
	}
	if tot, used := parseSwapUsage("total = 2048.00M  used = 1024.50M  free = 1023.50M  (encrypted)"); tot != 2048<<20 || used != uint64(1024.5*(1<<20)) {
		t.Fatalf("swap %d %d", tot, used)
	}

	ifs := parseNetstatIbn(`Name       Mtu   Network       Address            Ipkts Ierrs     Ibytes    Opkts Oerrs     Obytes  Coll
lo0        16384 <Link#1>                         1234     0     567890     1234     0     567890     0
lo0        16384 127           127.0.0.1          1234     -     567890     1234     -     567890     -
en0        1500  <Link#6>    a4:83:e7:12:34:56  100000     3  123456789    90000     1   98765432     0
en0        1500  192.168.1     192.168.1.20      100000     -  123456789    90000     -   98765432     -
en5*       1500  <Link#9>    00:e0:4c:68:0e:59       0     0          0        0     0          0     0
utun3      1380  <Link#20>                          500     0      40000      600     0      50000     0
`)
	if len(ifs) != 4 {
		t.Fatalf("netstat -ibn: %+v", ifs)
	}
	en0 := ifs[1]
	if en0.name != "en0" || !en0.up || en0.mac != "a4:83:e7:12:34:56" || en0.rxBytes != 123456789 || en0.rxErrs != 3 || en0.txBytes != 98765432 || en0.txErrs != 1 {
		t.Fatalf("en0 %+v", en0)
	}
	if ifs[2].name != "en5" || ifs[2].up || ifs[3].rxBytes != 40000 || ifs[0].rxBytes != 567890 {
		t.Fatalf("others %+v", ifs)
	}

	med := parseIfconfigMedia("en0: flags=8863<UP> mtu 1500\n\tether a4:83:e7:12:34:56\n\tmedia: autoselect (1000baseT <full-duplex>)\n\tstatus: active\nen1: flags=8863<UP> mtu 1500\n\tmedia: autoselect\n\tstatus: active\nen7: flags=8863<UP> mtu 1500\n\tmedia: autoselect (10Gbase-T <full-duplex>)\n\tstatus: inactive\n")
	if med["en0"] != [2]uint64{1, 1e9} || med["en1"] != [2]uint64{1, 0} || med["en7"] != [2]uint64{0, 10e9} {
		t.Fatalf("ifconfig %+v", med)
	}

	df := parseDfK("Filesystem     1024-blocks      Used Available Capacity iused ifree %iused  Mounted on\n/dev/disk3s1s1   494384795  10000000 300000000     4%  400000 3000000000    0%   /\n/dev/disk5s1   976000000 500000000 476000000    52%   1  2   1%   /Volumes/My Backup\n")
	if len(df) != 2 || df[1].mount != "/Volumes/My Backup" || df[0].free != 300000000*1024 {
		t.Fatalf("df %+v", df)
	}

	ps := parsePs("    1   0.0  12345 /sbin/launchd\n  500  37.5 204800 /Applications/Google Chrome.app/Contents/MacOS/Google Chrome\n")
	if len(ps) != 2 || ps[1].cpu != 37.5 || ps[1].rss != 204800*1024 || ps[1].comm != "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" {
		t.Fatalf("ps %+v", ps)
	}

	ns := parseNetstatS(`tcp:
	123456 packets sent
		100000 data packets (98765432 bytes)
		1234 data packets (567890 bytes) retransmitted
	234567 packets received
		12 discarded for bad checksums
	5 connections dropped
udp:
	34567 datagrams received
		89 dropped due to full socket buffers
	45678 datagrams output
ip:
	999 total packets received
`)
	if !ns.ok || ns.tcpSent != 123456 || ns.tcpRetrans != 1234 || ns.tcpRecv != 234567 || ns.tcpBadSum != 12 || ns.udpRecv != 34567 || ns.udpFull != 89 || ns.udpOut != 45678 {
		t.Fatalf("netstat -s %+v", ns)
	}
}

// macOS honours the user's numeric locale, including decimal commas.
func TestMacDecimalComma(t *testing.T) {
	ps := parsePs("613 40,6 107472 /System/Library/WindowServer\n1502 11,7 34928 /Applications/Example App.app/Contents/MacOS/App\n")
	if len(ps) != 2 || ps[0].cpu != 40.6 || ps[1].cpu != 11.7 || ps[0].rss != 107472*1024 {
		t.Fatalf("locale-specific process stats: %+v", ps)
	}
	for _, value := range []string{"40.6", "40,6"} {
		if got := atof(value); got != 40.6 {
			t.Errorf("atof(%q) = %v", value, got)
		}
	}
	if total, used := parseSwapUsage("total = 2048,00M used = 1024,50M free = 1023,50M"); total != 2048<<20 || used != uint64(1024.5*(1<<20)) {
		t.Fatalf("locale-specific swap: %d %d", total, used)
	}
}
