// Package web 提供内嵌的实时监控页面与 JSON API。
package web

import (
	"embed"
	"encoding/json"
	"net/http"
	"time"

	"qostool/internal/config"
	"qostool/internal/stats"
)

//go:embed static/index.html
var staticFS embed.FS

// Server 提供 /api/stats 与静态页面。
type Server struct {
	cfg *config.Config
	agg *stats.Aggregator
}

// New 创建 Web 服务。
func New(cfg *config.Config, agg *stats.Aggregator) *Server {
	return &Server{cfg: cfg, agg: agg}
}

// ListenAndServe 启动 HTTP 服务（addr 如 ":16666"）。
func (s *Server) ListenAndServe(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/stats", s.handleStats)
	mux.HandleFunc("/", s.handleIndex)
	return http.ListenAndServe(addr, mux)
}

type apiStats struct {
	Now     int64      `json:"now"`
	Flows   []apiFlow  `json:"flows"`
	History apiHistory `json:"history"`
}

type apiFlow struct {
	Idx       int     `json:"idx"`
	Name      string  `json:"name"`
	DSCP      int     `json:"dscp"`
	SrcIP     string  `json:"src_ip"`
	DstIP     string  `json:"dst_ip"`
	SrcPort   int     `json:"src_port"`
	DstPort   int     `json:"dst_port"`
	TxBps     float64 `json:"tx_bps"`
	TxPps     float64 `json:"tx_pps"`
	RxBps     float64 `json:"rx_bps"`
	RxPps     float64 `json:"rx_pps"`
	TxPackets uint64  `json:"tx_packets"`
	TxBytes   uint64  `json:"tx_bytes"`
	RxPackets uint64  `json:"rx_packets"`
	RxBytes   uint64  `json:"rx_bytes"`
	Lost      uint64  `json:"lost"`
	LossRate  float64 `json:"loss_rate"`
}

type apiHistory struct {
	T  []int64     `json:"t"`
	Tx [][]float64 `json:"tx"`
	Rx [][]float64 `json:"rx"`
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	snap := s.agg.Current(time.Now())
	hist := s.agg.History()
	out := apiStats{
		Now:     time.Now().UnixMilli(),
		Flows:   make([]apiFlow, len(snap)),
		History: apiHistory{T: hist.T, Tx: hist.TxB, Rx: hist.RxB},
	}
	for i, f := range s.cfg.Flows {
		sf := snap[i]
		out.Flows[i] = apiFlow{
			Idx: i, Name: f.Name, DSCP: int(f.DSCP),
			SrcIP: f.SrcIP, DstIP: f.DstIP, SrcPort: f.SrcPort, DstPort: f.DstPort,
			TxBps: sf.TxBps, TxPps: sf.TxPps, RxBps: sf.RxBps, RxPps: sf.RxPps,
			TxPackets: sf.TxPackets, TxBytes: sf.TxBytes,
			RxPackets: sf.RxPackets, RxBytes: sf.RxBytes,
			Lost: sf.Lost, LossRate: sf.LossRate,
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}
