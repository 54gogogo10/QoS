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
