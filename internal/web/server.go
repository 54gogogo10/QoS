// Package web 提供内嵌的实时监控页面与 JSON API。
package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gopacket/gopacket/pcap"

	"qostool/internal/config"
	"qostool/internal/controller"
)

//go:embed static/index.html
var staticFS embed.FS

// Server 提供页面与 JSON API：监控 + 运行控制（接口选择、启停、配置编辑）。
type Server struct {
	ctrl          *controller.Controller
	cfgPath       string // 配置持久化路径；空串表示不保存
	remoteControl bool   // true=页面可启停/改配置（app 模式）；false=只读监控（CLI 模式）
	srv           *http.Server
}

// New 创建 Web 服务。
func New(ctrl *controller.Controller, cfgPath string, remoteControl bool) *Server {
	return &Server{ctrl: ctrl, cfgPath: cfgPath, remoteControl: remoteControl}
}

// ListenAndServe 启动 HTTP 服务（addr 如 "127.0.0.1:16666"）。
func (s *Server) ListenAndServe(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/stats", s.handleStats)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/interfaces", s.handleInterfaces)
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/start", s.handleStart)
	mux.HandleFunc("/api/stop", s.handleStop)
	mux.HandleFunc("/", s.handleIndex)
	s.srv = &http.Server{Addr: addr, Handler: mux}
	return s.srv.ListenAndServe()
}

// Shutdown 优雅停止 HTTP 服务。
func (s *Server) Shutdown(ctx context.Context) error {
	if s.srv == nil {
		return nil
	}
	return s.srv.Shutdown(ctx)
}

// ---------- API 结构 ----------

type apiStats struct {
	Now     int64      `json:"now"`
	Running bool       `json:"running"`
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

type apiConfigFlow struct {
	Name        string  `json:"name"`
	Protocol    string  `json:"protocol"`
	SrcIP       string  `json:"src_ip"`
	DstIP       string  `json:"dst_ip"`
	SrcPort     int     `json:"src_port"`
	DstPort     int     `json:"dst_port"`
	DSCP        string  `json:"dscp"` // 数字或名字
	RateMbps    float64 `json:"rate_mbps"`
	RatePPS     float64 `json:"rate_pps"`
	PayloadSize int     `json:"payload_size"`
}

type apiConfig struct {
	Flows []apiConfigFlow `json:"flows"`
}

// toConfig 把 API 配置转成内部配置并校验。
func (a *apiConfig) toConfig() (*config.Config, error) {
	if len(a.Flows) == 0 {
		return nil, fmt.Errorf("flows 不能为空")
	}
	if len(a.Flows) > 8 {
		return nil, fmt.Errorf("最多支持 8 条流，当前 %d 条", len(a.Flows))
	}
	cfg := &config.Config{}
	for i, f := range a.Flows {
		dscp, err := config.ParseDSCP(f.DSCP)
		if err != nil {
			return nil, fmt.Errorf("flow %d (%s): %v", i+1, f.Name, err)
		}
		cfg.Flows = append(cfg.Flows, config.Flow{
			Name: f.Name, Protocol: f.Protocol,
			SrcIP: f.SrcIP, DstIP: f.DstIP,
			SrcPort: f.SrcPort, DstPort: f.DstPort,
			DSCP:        config.DSCP(dscp),
			RateMbps:    f.RateMbps,
			RatePPS:     f.RatePPS,
			PayloadSize: f.PayloadSize,
		})
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (s *Server) configToAPI(cfg *config.Config) apiConfig {
	out := apiConfig{Flows: make([]apiConfigFlow, len(cfg.Flows))}
	for i, f := range cfg.Flows {
		out.Flows[i] = apiConfigFlow{
			Name: f.Name, Protocol: f.Protocol,
			SrcIP: f.SrcIP, DstIP: f.DstIP,
			SrcPort: f.SrcPort, DstPort: f.DstPort,
			DSCP:        dscpDisplay(f.DSCP),
			RateMbps:    f.RateMbps,
			RatePPS:     f.RatePPS,
			PayloadSize: f.PayloadSize,
		}
	}
	return out
}

func dscpDisplay(d config.DSCP) string {
	if n := d.Name(); n != "" {
		return n
	}
	return fmt.Sprintf("%d", d)
}

// ---------- Handlers ----------

func (s *Server) requireControl(w http.ResponseWriter, r *http.Request) bool {
	if !s.remoteControl {
		writeJSONError(w, http.StatusForbidden, "命令行模式下页面控制已禁用")
		return false
	}
	return true
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	now := time.Now()
	agg := s.ctrl.Aggregator()
	cfg := s.ctrl.Config()
	status := s.ctrl.Status()
	out := apiStats{
		Now:     now.UnixMilli(),
		Running: status.Running,
		Flows:   make([]apiFlow, 0, len(cfg.Flows)),
		History: apiHistory{T: []int64{}, Tx: [][]float64{}, Rx: [][]float64{}},
	}
	if agg != nil {
		snap := agg.Current(now)
		hist := agg.History()
		out.Flows = make([]apiFlow, len(snap))
		out.History = apiHistory{T: hist.T, Tx: hist.TxB, Rx: hist.RxB}
		for i, f := range cfg.Flows {
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
	}
	writeJSON(w, out)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	st := s.ctrl.Status()
	writeJSON(w, struct {
		Running       bool      `json:"running"`
		Iface         string    `json:"iface"`
		Started       time.Time `json:"started"`
		RemoteControl bool      `json:"remote_control"`
		Mode          string    `json:"mode"`
	}{
		Running: st.Running, Iface: st.Iface, Started: st.Started,
		RemoteControl: s.remoteControl, Mode: string(s.ctrl.Mode()),
	})
}

func (s *Server) handleInterfaces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	devs, err := pcap.FindAllDevs()
	if err != nil {
		http.Error(w, "枚举接口失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	type apiIface struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Addresses   []string `json:"addresses"`
	}
	out := make([]apiIface, 0, len(devs))
	for _, d := range devs {
		addrs := make([]string, 0, len(d.Addresses))
		for _, a := range d.Addresses {
			if a.IP != nil && a.IP.String() != "0.0.0.0" && a.IP.String() != "::" {
				addrs = append(addrs, a.IP.String())
			}
		}
		out = append(out, apiIface{Name: d.Name, Description: d.Description, Addresses: addrs})
	}
	writeJSON(w, out)
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		writeJSON(w, s.configToAPI(s.ctrl.Config()))
	case http.MethodPost:
		if !s.requireControl(w, r) {
			return
		}
		var in apiConfig
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSONError(w, http.StatusBadRequest, "请求体解析失败: "+err.Error())
			return
		}
		cfg, err := in.toConfig()
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if s.cfgPath != "" {
			if err := cfg.Save(s.cfgPath); err != nil {
				writeJSONError(w, http.StatusInternalServerError, "保存配置文件失败: "+err.Error())
				return
			}
		}
		restarted, err := s.ctrl.UpdateConfig(cfg)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "应用配置失败: "+err.Error())
			return
		}
		writeJSON(w, struct {
			OK        bool   `json:"ok"`
			Restarted bool   `json:"restarted"`
			Message   string `json:"message"`
		}{OK: true, Restarted: restarted, Message: "配置已保存"})
	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.requireControl(w, r) {
		return
	}
	var in struct {
		Iface string `json:"iface"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONError(w, http.StatusBadRequest, "请求体解析失败: "+err.Error())
		return
	}
	if in.Iface == "" {
		writeJSONError(w, http.StatusBadRequest, "缺少 iface 参数")
		return
	}
	// 诊断：回环配置 + 非回环接口 → 必然收不到流量，提前给出明确提示
	if !isLoopbackIface(in.Iface) {
		for _, f := range s.ctrl.Config().Flows {
			if isLoopbackIP(f.SrcIP) || isLoopbackIP(f.DstIP) {
				writeJSONError(w, http.StatusBadRequest,
					"配置的流量目标是回环地址（"+f.SrcIP+"→"+f.DstIP+"），但所选接口 "+in.Iface+" 不是 Npcap 回环适配器，收不到流量。\n请选择 'Adapter for loopback traffic capture'（NPF_Loopback），或把配置改成实际 IP")
				return
			}
		}
	}
	if err := s.ctrl.Start(in.Iface); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}{OK: true, Message: "测试已启动"})
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.requireControl(w, r) {
		return
	}
	s.ctrl.Stop()
	writeJSON(w, struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}{OK: true, Message: "测试已停止"})
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

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func isLoopbackIface(name string) bool {
	l := strings.ToLower(name)
	return strings.Contains(l, "loopback")
}

func isLoopbackIP(ip string) bool {
	return ip == "127.0.0.1" || ip == "::1" || strings.HasPrefix(ip, "127.")
}

// writeJSONError 以 JSON 格式返回错误（前端可直接读取 error 字段）。
func writeJSONError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(struct {
		Error string `json:"error"`
	}{Error: msg})
}
