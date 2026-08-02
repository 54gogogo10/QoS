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
	PayloadSize int     `yaml:"payload_size"`
}

// Config 是一份完整配置，收发两端共用同一份。
type Config struct {
	Flows []Flow `yaml:"flows"`
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
		if f.PayloadSize < 0 {
			return fmt.Errorf("flow %d (%s): payload_size 不能为负", i+1, f.Name)
		}
	}
	return nil
}
