package config

import (
	"fmt"
	"net"
	"os"

	"gopkg.in/yaml.v3"
)

// Flow 描述一条测试流。
type Flow struct {
	Name        string  `yaml:"name"`
	Protocol    string  `yaml:"protocol"`
	SrcIP       string  `yaml:"src_ip"`
	DstIP       string  `yaml:"dst_ip"`
	SrcPort     int     `yaml:"src_port"`
	DstPort     int     `yaml:"dst_port"`
	DSCP        DSCP    `yaml:"dscp"`
	RateMbps    float64 `yaml:"rate_mbps"`
	RatePPS     float64 `yaml:"rate_pps"`
	IPLen       int     `yaml:"ip_len"` // IP 包总长（IP 头+UDP 头+载荷）
}

// Config 是一份完整配置，收发两端共用同一份。
type Config struct {
	Flows []Flow `yaml:"flows"`
}

// Save 把配置写回 YAML 文件。
func (c *Config) Save(path string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// DefaultConfig 返回内置默认配置：8 条 127.0.0.1 环回流，
// 覆盖常用 DSCP 优先级，开箱即能在任意机器跑通。
func DefaultConfig() *Config {
	flows := []struct {
		name  string
		dscp  int
		mbps  float64
		pps   float64
		ipLen int
	}{
		{"语音-EF", 46, 2, 0, 188},    // 原 160 载荷 + 28 IP/UDP 头
		{"视频-AF41", 34, 8, 0, 1228},
		{"交互-AF31", 26, 4, 0, 540},
		{"批量-AF21", 18, 10, 0, 1052},
		{"信令-CS6", 48, 1, 0, 228},
		{"会议-CS5", 40, 0, 500, 328},
		{"尽力而为-CS4", 32, 20, 0, 1428},
		{"背景-BE", 0, 0, 20000, 1428},
	}
	cfg := &Config{}
	for i, f := range flows {
		cfg.Flows = append(cfg.Flows, Flow{
			Name: f.name, Protocol: "udp",
			SrcIP: "127.0.0.1", DstIP: "127.0.0.1",
			SrcPort: 30000 + i, DstPort: 40000 + i,
			DSCP: DSCP(f.dscp), RateMbps: f.mbps, RatePPS: f.pps, IPLen: f.ipLen,
		})
	}
	return cfg
}

// Load 读取、解析并校验 YAML 配置文件。
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析配置 %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("配置校验失败: %w", err)
	}
	return &cfg, nil
}

// Validate 校验全部流的字段。
func (c *Config) Validate() error {
	if len(c.Flows) == 0 {
		return fmt.Errorf("flows 不能为空")
	}
	if len(c.Flows) > 8 {
		return fmt.Errorf("最多支持 8 条流，当前 %d 条", len(c.Flows))
	}
	for i := range c.Flows {
		f := &c.Flows[i]
		if f.Name == "" {
			f.Name = fmt.Sprintf("flow-%d", i+1)
		}
		if f.Protocol != "udp" {
			return fmt.Errorf("flow %d (%s): 仅支持 udp 协议", i+1, f.Name)
		}
		src := net.ParseIP(f.SrcIP)
		dst := net.ParseIP(f.DstIP)
		if src == nil {
			return fmt.Errorf("flow %d (%s): 非法 src_ip %q", i+1, f.Name, f.SrcIP)
		}
		if dst == nil {
			return fmt.Errorf("flow %d (%s): 非法 dst_ip %q", i+1, f.Name, f.DstIP)
		}
		if (src.To4() == nil) != (dst.To4() == nil) {
			return fmt.Errorf("flow %d (%s): src_ip 与 dst_ip 必须同为 IPv4 或同为 IPv6", i+1, f.Name)
		}
		if f.SrcPort < 1 || f.SrcPort > 65535 || f.DstPort < 1 || f.DstPort > 65535 {
			return fmt.Errorf("flow %d (%s): 端口必须在 1-65535", i+1, f.Name)
		}
		if f.RateMbps < 0 || f.RatePPS < 0 {
			return fmt.Errorf("flow %d (%s): 速率不能为负", i+1, f.Name)
		}
		if f.RateMbps == 0 && f.RatePPS == 0 {
			return fmt.Errorf("flow %d (%s): rate_mbps 与 rate_pps 至少填一个", i+1, f.Name)
		}
		minIPLen := 38 // IPv4: 20 IP + 8 UDP + 10 协议头
		if src.To4() == nil {
			minIPLen = 58 // IPv6: 40 + 8 + 10
		}
		if f.IPLen < minIPLen {
			return fmt.Errorf("flow %d (%s): ip_len %d 小于最小值 %d（IP/UDP 头 + 10 字节协议头）", i+1, f.Name, f.IPLen, minIPLen)
		}
		if f.IPLen > 65535 {
			return fmt.Errorf("flow %d (%s): ip_len %d 超过上限 65535", i+1, f.Name, f.IPLen)
		}
	}
	return nil
}
