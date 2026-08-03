// Package web 提供内嵌的实时监控页面与 JSON API。
package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"qostool/internal/config"
	"qostool/internal/controller"
)

//go:embed static/index.html
var staticFS embed.FS

// Server 提供页面与 JSON API：监控 + 运行控制（接口选择、启停、配置编辑）。
type Server struct {
	ctrls         map[controller.Mode]*controller.Controller // 每角色独立实例
	cfgPaths      map[controller.Mode]string                // 每角色独立配置文件
	remoteControl bool                                      // true=页面可启停/改配置（app 模式）；false=只读监控（CLI 模式）
	srv           *http.Server

	remoteMu          sync.Mutex
	remoteAddr        string // 发送端地址（IP:port），接收端拉取其 TX 统计
	remoteData        *remoteSnapshot
	remoteLoopRunning bool
}

// remoteSnapshot 是拉取到的发送端 TX 统计快照。
type remoteSnapshot struct {
	Online  bool      `json:"online"`
	Addr    string    `json:"addr"`
	Running bool      `json:"running"`
	Updated time.Time `json:"updated"`
	TxBps   []float64 `json:"tx_bps"`
	TxPps   []float64 `json:"tx_pps"`
	TxPkts  []uint64  `json:"tx_packets"`
	TxBytes []uint64  `json:"tx_bytes"`
}

// New 创建 Web 服务：send/recv/bidir 三个独立实例与独立配置文件。
func New(ctrls map[controller.Mode]*controller.Controller, cfgPaths map[controller.Mode]string, remoteControl bool) *Server {
	return &Server{ctrls: ctrls, cfgPaths: cfgPaths, remoteControl: remoteControl}
}

// ctrl 返回指定角色的控制器（CLI 单实例模式下返回唯一实例）。
func (s *Server) ctrl(m controller.Mode) *controller.Controller {
	if c := s.ctrls[m]; c != nil {
		return c
	}
	// 兼容单实例（CLI）：任意模式返回唯一实例
	for _, c := range s.ctrls {
		return c
	}
	return nil
}

// SetRemote 设置发送端地址（IP:port），供 CLI 模式通过 --remote 参数使用。
func (s *Server) SetRemote(addr string) {
	s.remoteMu.Lock()
	s.remoteAddr = addr
	s.remoteData = nil
	s.remoteMu.Unlock()
	if addr != "" {
		s.startRemoteLoop()
	}
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
	mux.HandleFunc("/api/mode", s.handleMode)
	mux.HandleFunc("/api/remote", s.handleRemote)
	mux.HandleFunc("/api/tx_stats", s.handleTxStats)
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

// apiIface 是接口信息（API 返回结构），由平台文件 listInterfaces() 填充。
type apiIface struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Addresses   []string `json:"addresses"`
}

type apiStats struct {
	Now     int64           `json:"now"`
	Running bool            `json:"running"`
	Flows   []apiFlow       `json:"flows"`
	History apiHistory      `json:"history"`
	Remote  *remoteSnapshot `json:"remote,omitempty"`
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
	IPLen       int     `json:"ip_len"` // IP 包总长
}

type apiConfig struct {
	Flows []apiConfigFlow `json:"flows"`
}

// toConfig 把 API 配置转成内部配置并校验。
func (a *apiConfig) toConfig(requireRates bool) (*config.Config, error) {
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
			RateMbps: f.RateMbps,
			RatePPS:  f.RatePPS,
			IPLen:    f.IPLen,
		})
	}
	if requireRates {
		if err := cfg.Validate(); err != nil {
			return nil, err
		}
	} else if err := cfg.ValidateRecv(); err != nil {
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
			RateMbps: f.RateMbps,
			RatePPS:  f.RatePPS,
			IPLen:    f.IPLen,
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
	m := modeFromQuery(r)
	ctrl := s.ctrl(m)
	now := time.Now()
	agg := ctrl.Aggregator()
	cfg := ctrl.Config()
	status := ctrl.Status()
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
	// 附加远端发送端 TX 统计（接收端角色统一显示；离线时也返回 online=false 供状态指示）
	s.remoteMu.Lock()
	if s.remoteData != nil && s.remoteAddr != "" {
		out.Remote = s.remoteData
	}
	s.remoteMu.Unlock()
	writeJSON(w, out)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	type roleStatus struct {
		Running bool      `json:"running"`
		Iface   string    `json:"iface"`
		Started time.Time `json:"started"`
		LogFile string    `json:"log_file"`
	}
	out := struct {
		Send          roleStatus `json:"send"`
		Recv          roleStatus `json:"recv"`
		Bidir         roleStatus `json:"bidir"`
		RemoteControl bool       `json:"remote_control"`
		Mode          string     `json:"mode"`
	}{
		RemoteControl: s.remoteControl,
		Mode:          r.URL.Query().Get("mode"),
	}
	for _, m := range []controller.Mode{controller.ModeSend, controller.ModeRecv, controller.ModeBidir} {
		st := s.ctrl(m).Status()
		rs := roleStatus{Running: st.Running, Iface: st.Iface, Started: st.Started, LogFile: st.LogFile}
		switch m {
		case controller.ModeSend:
			out.Send = rs
		case controller.ModeRecv:
			out.Recv = rs
		case controller.ModeBidir:
			out.Bidir = rs
		}
	}
	writeJSON(w, out)
}

func (s *Server) handleInterfaces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	devs, err := listInterfaces()
	if err != nil {
		http.Error(w, "枚举接口失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, devs)
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	m := modeFromQuery(r)
	ctrl := s.ctrl(m)
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		writeJSON(w, s.configToAPI(ctrl.Config()))
	case http.MethodPost:
		if !s.requireControl(w, r) {
			return
		}
		var in apiConfig
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSONError(w, http.StatusBadRequest, "请求体解析失败: "+err.Error())
			return
		}
		requireRates := m != controller.ModeRecv
		cfg, err := in.toConfig(requireRates)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if p := s.cfgPaths[m]; p != "" {
			if err := cfg.Save(p); err != nil {
				writeJSONError(w, http.StatusInternalServerError, "保存配置文件失败: "+err.Error())
				return
			}
		}
		restarted, err := ctrl.UpdateConfig(cfg)
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
		Mode  string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONError(w, http.StatusBadRequest, "请求体解析失败: "+err.Error())
		return
	}
	m := modeFromString(in.Mode)
	ctrl := s.ctrl(m)
	if in.Iface == "" && m != controller.ModeSend {
		writeJSONError(w, http.StatusBadRequest, "缺少 iface 参数")
		return
	}
	// 诊断：回环配置 + 非回环接口 → 必然收不到流量，提前给出明确提示（发送角色不抓包，跳过）
	if m != controller.ModeSend && !isLoopbackIface(in.Iface) {
		for _, f := range ctrl.Config().Flows {
			if isLoopbackIP(f.SrcIP) || isLoopbackIP(f.DstIP) {
				writeJSONError(w, http.StatusBadRequest,
					"配置的流量目标是回环地址（"+f.SrcIP+"→"+f.DstIP+"），但所选接口 "+in.Iface+" 不是 Npcap 回环适配器，收不到流量。\n请选择 'Adapter for loopback traffic capture'（NPF_Loopback），或把配置改成实际 IP")
				return
			}
		}
	}
	if err := ctrl.Start(in.Iface); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}{OK: true, Message: "已启动"})
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.requireControl(w, r) {
		return
	}
	var in struct {
		Mode string `json:"mode"`
	}
	json.NewDecoder(r.Body).Decode(&in)
	s.ctrl(modeFromString(in.Mode)).Stop()
	writeJSON(w, struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}{OK: true, Message: "已停止"})
}

// handleMode 切换角色：send（只发送）/ recv（只监听）/ bidir（双向）。
func (s *Server) handleMode(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, struct {
		OK      bool     `json:"ok"`
		Message string   `json:"message"`
		Modes   []string `json:"modes"`
	}{OK: true, Message: "角色已独立运行，无需切换", Modes: []string{"send", "recv", "bidir"}})
}

// handleRemote 设置发送端地址（接收端拉取其 TX 统计并统一显示）。
// 空地址清除。
func (s *Server) handleRemote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.requireControl(w, r) {
		return
	}
	var in struct {
		Addr string `json:"addr"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONError(w, http.StatusBadRequest, "请求体解析失败: "+err.Error())
		return
	}
	in.Addr = strings.TrimSpace(in.Addr)
	if in.Addr != "" && !strings.Contains(in.Addr, ":") {
		in.Addr += ":16666" // 默认端口
	}
	s.remoteMu.Lock()
	s.remoteAddr = in.Addr
	s.remoteData = nil
	s.remoteMu.Unlock()
	if in.Addr != "" {
		s.startRemoteLoop()
	}
	writeJSON(w, struct {
		OK   bool   `json:"ok"`
		Addr string `json:"addr"`
	}{OK: true, Addr: in.Addr})
}

// remoteLoopOnce 拉取一次发送端 TX 统计（供轮询 goroutine 调用）。
func (s *Server) remoteLoopOnce() {
	s.remoteMu.Lock()
	addr := s.remoteAddr
	if addr == "" {
		s.remoteMu.Unlock()
		return
	}
	s.remoteMu.Unlock()

	snap := &remoteSnapshot{Addr: addr}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + addr + "/api/tx_stats")
	if err != nil {
		// 拉取失败（发送端退出/网络断）：保留最后一次成功的数据，仅标记离线，
		// 让接收端仍能显示发送端停止前的累计包数。
		s.remoteMu.Lock()
		if s.remoteData == nil {
			s.remoteData = snap // 从未成功过
		} else {
			s.remoteData.Online = false
			s.remoteData.Running = false
		}
		s.remoteMu.Unlock()
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		var out struct {
			Running bool     `json:"running"`
			TxBps   []float64 `json:"tx_bps"`
			TxPps   []float64 `json:"tx_pps"`
			TxPkts  []uint64  `json:"tx_packets"`
			TxBytes []uint64  `json:"tx_bytes"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err == nil {
			snap.Online = true
			snap.Running = out.Running
			snap.TxBps = out.TxBps
			snap.TxPps = out.TxPps
			snap.TxPkts = out.TxPkts
			snap.TxBytes = out.TxBytes
			snap.Updated = time.Now()
		}
	}
	s.remoteMu.Lock()
	s.remoteData = snap
	s.remoteMu.Unlock()
}

// startRemoteLoop 启动每秒轮询发送端统计的 goroutine（幂等）。
func (s *Server) startRemoteLoop() {
	s.remoteMu.Lock()
	defer s.remoteMu.Unlock()
	if s.remoteLoopRunning {
		return
	}
	s.remoteLoopRunning = true
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for range ticker.C {
			s.remoteMu.Lock()
			addr := s.remoteAddr
			s.remoteMu.Unlock()
			if addr == "" {
				s.remoteMu.Lock()
				s.remoteLoopRunning = false
				s.remoteMu.Unlock()
				return
			}
			s.remoteLoopOnce()
		}
	}()
}

// handleTxStats 供远端接收端拉取本端 TX 统计：
// 优先选择发送端角色（send）有数据的实例（含停止后保留的累计值），其次双向（bidir）。
func (s *Server) handleTxStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	var ctrl *controller.Controller
	for _, m := range []controller.Mode{controller.ModeSend, controller.ModeBidir} {
		c := s.ctrl(m)
		// 有聚合器即有数据：运行中或停止后保留的累计统计
		if c.Aggregator() != nil {
			ctrl = c
			break
		}
	}
	out := struct {
		Running bool      `json:"running"`
		TxBps   []float64 `json:"tx_bps"`
		TxPps   []float64 `json:"tx_pps"`
		TxPkts  []uint64  `json:"tx_packets"`
		TxBytes []uint64  `json:"tx_bytes"`
	}{
		TxBps:   []float64{}, TxPps: []float64{}, TxPkts: []uint64{}, TxBytes: []uint64{},
	}
	if ctrl != nil {
		out.Running = ctrl.Status().Running
		agg := ctrl.Aggregator()
		if agg != nil {
			snap := agg.Current(time.Now())
			for i := range snap {
				out.TxBps = append(out.TxBps, snap[i].TxBps)
				out.TxPps = append(out.TxPps, snap[i].TxPps)
				out.TxPkts = append(out.TxPkts, snap[i].TxPackets)
				out.TxBytes = append(out.TxBytes, snap[i].TxBytes)
			}
		}
	}
	writeJSON(w, out)
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

// modeFromQuery 从查询参数取角色；缺省 bidir。
func modeFromQuery(r *http.Request) controller.Mode {
	return modeFromString(r.URL.Query().Get("mode"))
}

// modeFromString 解析角色字符串；空或非法返回 bidir。
func modeFromString(m string) controller.Mode {
	switch m {
	case "send":
		return controller.ModeSend
	case "recv":
		return controller.ModeRecv
	default:
		return controller.ModeBidir
	}
}
