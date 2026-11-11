package web

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"packettrail/internal/peering"
)

// PeerNet is one external network in the peering view.
type PeerNet struct {
	ASN       uint32          `json:"asn"`
	Org       string          `json:"org"`
	CC        string          `json:"cc"`
	IPs       int             `json:"ips"`
	Apps      string          `json:"apps"`
	In        uint64          `json:"in"`
	Out       uint64          `json:"out"`
	Total     uint64          `json:"total"`
	Share     float64         `json:"share"` // % of off-net traffic
	RateIn    float64         `json:"rateIn"`
	RateOut   float64         `json:"rateOut"`
	First     int64           `json:"first"`
	Last      int64           `json:"last"`
	PDB       *peering.Record `json:"pdb"`
	Pending   bool            `json:"pending"`
	Common    []peering.IX    `json:"common"`
	Candidate string          `json:"candidate"` // short peering hint
	quality
}

// PeeringView is the whole peering tab.
type PeeringView struct {
	Own       peering.Own       `json:"own"`
	OwnPDB    []*peering.Record `json:"ownPdb"`
	Detecting bool              `json:"detecting"`
	Local     uint64            `json:"local"`
	OnNet     uint64            `json:"onNet"`
	OffNet    uint64            `json:"offNet"`
	Networks  []PeerNet         `json:"networks"`
	PDBError  string            `json:"pdbError,omitempty"`
}

const pdbLookups = 25 // networks looked up in PeeringDB per view

func (s *Server) peeringView(p Period) (*PeeringView, error) {
	m, meta, err := s.ipAggs(p)
	if err != nil {
		return nil, err
	}
	det, pdb := s.opts.Peering, s.opts.PeeringDB
	v := &PeeringView{Own: det.Current(), Detecting: det.Busy(), Networks: []PeerNet{}}
	own := map[uint32]bool{}
	ownIX := map[int]bool{}
	for _, a := range v.Own.ASNs {
		own[a] = true
		rec, _ := pdb.Get(a)
		if rec != nil {
			v.OwnPDB = append(v.OwnPDB, rec)
			for _, x := range rec.IXes {
				ownIX[x.ID] = true
			}
		}
	}
	name := func(k string) string {
		if n := meta[k].Name; n != "" {
			return n
		}
		return k
	}
	type agg struct {
		PeerNet
		apps map[string]uint64
		cc   map[string]uint64
		q    qacc
	}
	by := map[uint32]*agg{}
	for ip, a := range m {
		info := s.enr.Lookup(ip)
		tot := a.In + a.Out
		switch {
		case info.Scope != "":
			v.Local += tot
			continue
		case own[info.ASN]:
			v.OnNet += tot
			continue
		}
		v.OffNet += tot
		if info.ASN == 0 {
			continue
		}
		g := by[info.ASN]
		if g == nil {
			g = &agg{PeerNet: PeerNet{ASN: info.ASN, Org: info.ASOrg, First: a.First}, apps: map[string]uint64{}, cc: map[string]uint64{}}
			by[info.ASN] = g
		}
		g.IPs++
		g.In += a.In
		g.Out += a.Out
		g.RateIn += a.RateIn
		g.RateOut += a.RateOut
		g.q.merge(a.q)
		if a.First < g.First {
			g.First = a.First
		}
		if a.Last > g.Last {
			g.Last = a.Last
		}
		if info.CC != "" {
			g.cc[info.CC] += tot + 1
		}
		for k, n := range a.apps {
			g.apps[k] += n
		}
	}
	list := make([]*agg, 0, len(by))
	for _, g := range by {
		g.Total = g.In + g.Out
		list = append(list, g)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Total > list[j].Total })
	same := func(k string) string { return k }
	for i, g := range list {
		pn := g.PeerNet
		if v.OffNet > 0 {
			pn.Share = float64(int(10000*float64(pn.Total)/float64(v.OffNet))) / 100
		}
		pn.Apps, _ = topKeys(g.apps, 3, name)
		_, ccs := topKeys(g.cc, 1, same)
		if len(ccs) > 0 {
			pn.CC = ccs[0]
		}
		pn.quality = g.q.view()
		pn.Common = []peering.IX{}
		if i < pdbLookups {
			pn.PDB, pn.Pending = pdb.Get(pn.ASN)
		}
		if pn.PDB != nil && pn.PDB.Found {
			if pn.PDB.Name != "" {
				pn.Org = pn.PDB.Name
			}
			for _, x := range pn.PDB.IXes {
				if ownIX[x.ID] {
					pn.Common = append(pn.Common, x)
				}
			}
			sort.Slice(pn.Common, func(a, b int) bool { return pn.Common[a].Name < pn.Common[b].Name })
		}
		pn.Candidate = candidate(pn, len(v.OwnPDB) > 0)
		v.Networks = append(v.Networks, pn)
	}
	if len(v.Networks) > 200 {
		v.Networks = v.Networks[:200]
	}
	v.PDBError = pdb.LastError()
	return v, nil
}

// candidate summarises the peering opportunity for a network.
func candidate(pn PeerNet, ownKnown bool) string {
	r := pn.PDB
	switch {
	case r == nil:
		return ""
	case !r.Found:
		return "Not in PeeringDB"
	case !ownKnown:
		return ""
	case len(pn.Common) > 0 && strings.EqualFold(r.Policy, "Open"):
		return fmt.Sprintf("Open policy, %d common IX", len(pn.Common))
	case len(pn.Common) > 0:
		return fmt.Sprintf("%s policy, %d common IX", r.Policy, len(pn.Common))
	case len(r.IXes) > 0:
		return "No common IX"
	default:
		return "No public IX presence"
	}
}

func (s *Server) registerPeeringRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/peering", func(w http.ResponseWriter, r *http.Request) {
		s.opts.Peering.DetectIfStale()
		v, err := s.peeringView(parsePeriod(r.URL.Query().Get("period")))
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, v)
	})
	mux.HandleFunc("POST /api/peering/own", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, s.opts.Peering.SetManual(r.URL.Query().Get("asns")))
	})
	mux.HandleFunc("POST /api/peering/detect", func(w http.ResponseWriter, r *http.Request) {
		go s.opts.Peering.Detect()
		writeJSON(w, map[string]bool{"started": true})
	})
}
