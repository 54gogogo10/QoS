package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "flows.yaml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const validYAML = `
flows:
  - name: ef
    protocol: udp
    src_ip: 192.168.1.10
    dst_ip: 192.168.1.20
    src_port: 10000
    dst_port: 20000
    dscp: 46
    rate_mbps: 10
    ip_len: 156
  - name: af41
    protocol: udp
    src_ip: 192.168.1.10
    dst_ip: 192.168.1.20
    src_port: 10001
    dst_port: 20001
    dscp: AF41
    rate_pps: 1000
    ip_len: 92
`

func TestLoadValid(t *testing.T) {
	cfg, err := Load(writeTemp(t, validYAML))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Flows) != 2 {
		t.Fatalf("flows = %d, want 2", len(cfg.Flows))
	}
	if int(cfg.Flows[0].DSCP) != 46 {
		t.Fatalf("flow0 dscp = %d, want 46", cfg.Flows[0].DSCP)
	}
	if int(cfg.Flows[1].DSCP) != 34 {
		t.Fatalf("flow1 dscp = %d, want 34", cfg.Flows[1].DSCP)
	}
	if cfg.Flows[0].RateMbps != 10 || cfg.Flows[0].RatePPS != 0 {
		t.Fatalf("flow0 速率解析错误: %+v", cfg.Flows[0])
	}
}

func TestLoadErrors(t *testing.T) {
	cases := []struct{ name, yaml string }{
		{"空 flows", "flows: []"},
		{"超 8 条", "flows:\n" + flowLine() + flowLine() + flowLine() + flowLine() + flowLine() + flowLine() + flowLine() + flowLine() + flowLine()},
		{"无速率", "flows:\n  - name: a\n    protocol: udp\n    src_ip: 1.1.1.1\n    dst_ip: 2.2.2.2\n    src_port: 1\n    dst_port: 2\n    dscp: 0\n"},
		{"坏 IP", "flows:\n  - name: a\n    protocol: udp\n    src_ip: not-an-ip\n    dst_ip: 2.2.2.2\n    src_port: 1\n    dst_port: 2\n    dscp: 0\n    rate_pps: 100\n"},
		{"端口 0", "flows:\n  - name: a\n    protocol: udp\n    src_ip: 1.1.1.1\n    dst_ip: 2.2.2.2\n    src_port: 0\n    dst_port: 2\n    dscp: 0\n    rate_pps: 100\n"},
		{"家族不匹配", "flows:\n  - name: a\n    protocol: udp\n    src_ip: 1.1.1.1\n    dst_ip: ::1\n    src_port: 1\n    dst_port: 2\n    dscp: 0\n    rate_pps: 100\n"},
		{"dscp 越界", "flows:\n  - name: a\n    protocol: udp\n    src_ip: 1.1.1.1\n    dst_ip: 2.2.2.2\n    src_port: 1\n    dst_port: 2\n    dscp: 64\n    rate_pps: 100\n"},
	}
	for _, c := range cases {
		if _, err := Load(writeTemp(t, c.yaml)); err == nil {
			t.Fatalf("%s: 期望报错", c.name)
		}
	}
}

func flowLine() string {
	return "  - name: a\n    protocol: udp\n    src_ip: 1.1.1.1\n    dst_ip: 2.2.2.2\n    src_port: 1\n    dst_port: 2\n    dscp: 0\n    rate_pps: 100\n"
}

func TestDefaultConfigValid(t *testing.T) {
	cfg := DefaultConfig()
	if len(cfg.Flows) != 8 {
		t.Fatalf("默认配置应为 8 条流, got %d", len(cfg.Flows))
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("默认配置校验失败: %v", err)
	}
	// DSCP 应覆盖 46/34/26/18/48/40/32/0
	want := []int{46, 34, 26, 18, 48, 40, 32, 0}
	for i, w := range want {
		if int(cfg.Flows[i].DSCP) != w {
			t.Fatalf("flow %d DSCP = %d, want %d", i, cfg.Flows[i].DSCP, w)
		}
	}
}

func TestConfigSaveRoundTrip(t *testing.T) {
	cfg := DefaultConfig()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := cfg.Save(p); err != nil {
		t.Fatal(err)
	}
	cfg2, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg2.Flows) != 8 {
		t.Fatalf("往返后流数 = %d", len(cfg2.Flows))
	}
	// DSCP 名字应能往返（MarshalYAML 输出名字，UnmarshalYAML 读回）
	if int(cfg2.Flows[0].DSCP) != 46 {
		t.Fatalf("DSCP 往返失败: %d", cfg2.Flows[0].DSCP)
	}
}

// TestValidateRecvNoRates 接收端配置不要求速率与包长。
func TestValidateRecvNoRates(t *testing.T) {
	cfg := &Config{Flows: []Flow{
		{Name: "a", Protocol: "udp", SrcIP: "1.1.1.1", DstIP: "2.2.2.2",
			SrcPort: 100, DstPort: 200, DSCP: 46},
	}}
	if err := cfg.ValidateRecv(); err != nil {
		t.Fatalf("接收端配置应通过: %v", err)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("发送校验应要求速率")
	}
	// 接收端配置保存再加载（LoadRecv 往返）
	p := filepath.Join(t.TempDir(), "recv.yaml")
	if err := cfg.Save(p); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRecv(p); err != nil {
		t.Fatalf("LoadRecv 失败: %v", err)
	}
}

// TestThresholdFields 验证阈值字段的 yaml 往返与校验。
func TestThresholdFields(t *testing.T) {
	yaml := validYAML + "    max_loss_rate_pct: 0.5\n    max_avg_delay_ms: 50\n    max_jitter_ms: 10\n"
	cfg, err := Load(writeTemp(t, yaml))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Flows[1].MaxLossRatePct != 0.5 || cfg.Flows[1].MaxAvgDelayMs != 50 || cfg.Flows[1].MaxJitterMs != 10 {
		t.Fatalf("阈值字段 = %+v", cfg.Flows[1])
	}
	if cfg.Flows[0].MaxLossRatePct != 0 || cfg.Flows[0].MaxAvgDelayMs != 0 {
		t.Fatalf("缺省应为 0: %+v", cfg.Flows[0])
	}
	// 保存后重新加载（yaml 往返）
	p := writeTemp(t, yaml)
	cfg2, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg2.Save(p); err != nil {
		t.Fatal(err)
	}
	cfg3, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg3.Flows[1].MaxLossRatePct != 0.5 {
		t.Fatalf("往返后阈值丢失: %+v", cfg3.Flows[1])
	}
}

func TestThresholdValidation(t *testing.T) {
	bad := []struct{ name, field string }{
		{"负丢包阈值", "max_loss_rate_pct: -1"},
		{"丢包阈值超100", "max_loss_rate_pct: 101"},
		{"负时延阈值", "max_avg_delay_ms: -5"},
		{"负抖动阈值", "max_jitter_ms: -1"},
	}
	for _, b := range bad {
		yaml := "flows:\n  - name: a\n    protocol: udp\n    src_ip: 127.0.0.1\n    dst_ip: 127.0.0.1\n    src_port: 1000\n    dst_port: 2000\n    dscp: 0\n    rate_pps: 100\n    ip_len: 92\n    " + b.field + "\n"
		if _, err := Load(writeTemp(t, yaml)); err == nil {
			t.Fatalf("%s 应校验失败", b.name)
		}
	}
	// 接收端校验（无速率）同样拒绝非法阈值
	yaml := "flows:\n  - name: a\n    protocol: udp\n    src_ip: 127.0.0.1\n    dst_ip: 127.0.0.1\n    src_port: 1000\n    dst_port: 2000\n    dscp: 0\n    max_loss_rate_pct: 101\n"
	if _, err := LoadRecv(writeTemp(t, yaml)); err == nil {
		t.Fatal("接收端也应拒绝非法阈值")
	}
}

// TestMinIPLen 最小包长（v2.8.0）：18 字节协议头含发送时间戳，IPv4 最小 46、IPv6 最小 66。
func TestMinIPLen(t *testing.T) {
	mk := func(ip, port, ipLen string) string {
		return "flows:\n  - name: a\n    protocol: udp\n    src_ip: " + ip + "\n    dst_ip: " + ip +
			"\n    src_port: 1000\n    dst_port: " + port + "\n    dscp: 0\n    rate_pps: 100\n    ip_len: " + ipLen + "\n"
	}
	// IPv4：46 通过，45 拒绝
	if _, err := Load(writeTemp(t, mk("127.0.0.1", "2000", "46"))); err != nil {
		t.Fatalf("IPv4 ip_len=46 应通过: %v", err)
	}
	if _, err := Load(writeTemp(t, mk("127.0.0.1", "2000", "45"))); err == nil {
		t.Fatal("IPv4 ip_len=45 应拒绝（载荷不足 18 字节协议头）")
	}
	// IPv6：66 通过，65 拒绝
	if _, err := Load(writeTemp(t, mk("::1", "2000", "66"))); err != nil {
		t.Fatalf("IPv6 ip_len=66 应通过: %v", err)
	}
	if _, err := Load(writeTemp(t, mk("::1", "2000", "65"))); err == nil {
		t.Fatal("IPv6 ip_len=65 应拒绝")
	}
}
