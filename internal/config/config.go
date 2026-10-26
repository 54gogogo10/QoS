package config

import (
	"fmt"
	"math"
	"net"
	"os"

	"gopkg.in/yaml.v3"
)

// finite 校验浮点配置值必须是有限数（v2.10.0 审查修复）：
// YAML 支持 .nan/.inf 字面量，而 NaN 与任何比较均为 false，能绕过
// "< 0"/"> 上限"式校验；NaN 速率会让 pacing 计算得到未定义的时长。
func finite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

// Flow 描述一条测试流。
type Flow struct {
	Name     string  `yaml:"name"`
	Protocol string  `yaml:"protocol"`
	SrcIP    string  `yaml:"src_ip"`
	DstIP    string  `yaml:"dst_ip"`
	SrcPort  int     `yaml:"src_port"`
	DstPort  int     `yaml:"dst_port"`
	DSCP     DSCP    `yaml:"dscp"`
	RateMbps float64 `yaml:"rate_mbps,omitempty"`
	RatePPS  float64 `yaml:"rate_pps,omitempty"`
	IPLen    int     `yaml:"ip_len,omitempty"` // IP 包总长（IP 头+UDP 头+载荷）

	// 阈值判定（v2.7.0）：0/缺省 = 不判定
	MaxLossRatePct float64 `yaml:"max_loss_rate_pct,omitempty"` // 丢包率阈值 %
	MaxAvgDelayMs  float64 `yaml:"max_avg_delay_ms,omitempty"`  // 平均时延阈值 ms
	MaxJitterMs    float64 `yaml:"max_jitter_ms,omitempty"`     // 抖动阈值 ms

	// 阈值判定扩展（v2.11.0）：0/缺省 = 不判定
	MaxP95DelayMs float64 `yaml:"max_p95_delay_ms,omitempty"` // p95 时延阈值 ms（全程累计）
	MaxP99DelayMs float64 `yaml:"max_p99_delay_ms,omitempty"` // p99 时延阈值 ms（全程累计）
	MaxReorder    uint64  `yaml:"max_reorder,omitempty"`      // 乱序包数阈值（累计值）
}

// Config 是一份完整配置，收发两端共用同一份。
type Config struct {
	Flows []Flow `yaml:"flows"`

	// 阈值越限自动停止（v2.10.0，全局，0/缺省=禁用）：任一已配置阈值的流
	// 在实时口径下持续越限该时长（秒）后自动停止测试并生成报告。
	AutoStopSec float64 `yaml:"auto_stop_sec,omitempty"`
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
		{"语音-EF", 46, 2, 0, 188}, // 原 160 载荷 + 28 IP/UDP 头
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

// Validate 校验全部流的字段（发送相关参数：速率必填、ip_len 必须合法）。
func (c *Config) Validate() error {
	return c.validate(true)
}

// ValidateRecv 校验接收端配置：不要求速率与包长（只监听不发送）。
func (c *Config) ValidateRecv() error {
	return c.validate(false)
}

// validate 校验全部流的字段；requireRates=false 时跳过速率与 ip_len 检查（接收端）。
func (c *Config) validate(requireRates bool) error {
	if len(c.Flows) == 0 {
		return fmt.Errorf("flows 不能为空")
	}
	if len(c.Flows) > 8 {
		return fmt.Errorf("最多支持 8 条流，当前 %d 条", len(c.Flows))
	}
	if !finite(c.AutoStopSec) || c.AutoStopSec < 0 || c.AutoStopSec > 2592000 {
		return fmt.Errorf("auto_stop_sec 必须是 0-2592000（30 天）的有限数值，当前 %v", c.AutoStopSec)
	}
	for i := range c.Flows {
		f := &c.Flows[i]
		if f.Name == "" {
			f.Name = fmt.Sprintf("flow-%d", i+1)
		}
		// 流名长度上限（v2.10.0）：流名会进入终端表格/Web/HTML/CSV/metrics 标签，
		// 无上限的超长名可被控制 API（1MB body）注入并放大输出
		if len(f.Name) > 128 {
			return fmt.Errorf("flow %d: name 超过 128 字节上限", i+1)
		}
		if f.Protocol != "udp" {
			return fmt.Errorf("flow %d (%s): 仅支持 udp 协议", i+1, f.Name)
		}
		if !finite(f.MaxLossRatePct) || f.MaxLossRatePct < 0 || f.MaxLossRatePct > 100 {
			return fmt.Errorf("flow %d (%s): max_loss_rate_pct 必须是 0-100 的有限数值", i+1, f.Name)
		}
		if !finite(f.MaxAvgDelayMs) || f.MaxAvgDelayMs < 0 {
			return fmt.Errorf("flow %d (%s): max_avg_delay_ms 必须是有限非负数值", i+1, f.Name)
		}
		if !finite(f.MaxJitterMs) || f.MaxJitterMs < 0 {
			return fmt.Errorf("flow %d (%s): max_jitter_ms 必须是有限非负数值", i+1, f.Name)
		}
		// v2.11.0 阈值扩展：时延百分位（有限非负）
		if !finite(f.MaxP95DelayMs) || f.MaxP95DelayMs < 0 {
			return fmt.Errorf("flow %d (%s): max_p95_delay_ms 必须是有限非负数值", i+1, f.Name)
		}
		if !finite(f.MaxP99DelayMs) || f.MaxP99DelayMs < 0 {
			return fmt.Errorf("flow %d (%s): max_p99_delay_ms 必须是有限非负数值", i+1, f.Name)
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
		if !requireRates {
			continue // 接收端：无需速率与包长
		}
		if !finite(f.RateMbps) || !finite(f.RatePPS) || f.RateMbps < 0 || f.RatePPS < 0 {
			return fmt.Errorf("flow %d (%s): 速率必须是有限非负数值（mbps=%v pps=%v）", i+1, f.Name, f.RateMbps, f.RatePPS)
		}
		// 速率上限（远超任何真实网卡）：防止荒谬大量级把包间隔压到 1ns、
		// 批量膨胀成数十万包/批的无 pacing 洪泛
		if f.RateMbps > 1e6 || f.RatePPS > 1e9 {
			return fmt.Errorf("flow %d (%s): 速率超出上限（最大 1000000 Mbps / 1000000000 pps）", i+1, f.Name)
		}
		if f.RateMbps == 0 && f.RatePPS == 0 {
			return fmt.Errorf("flow %d (%s): rate_mbps 与 rate_pps 至少填一个", i+1, f.Name)
		}
		minIPLen := 46 // IPv4: 20 IP + 8 UDP + 18 协议头（v2.7.0 起含 8 字节发送时间戳）
		if src.To4() == nil {
			minIPLen = 66 // IPv6: 40 + 8 + 18
		}
		if f.IPLen < minIPLen {
			return fmt.Errorf("flow %d (%s): ip_len %d 小于最小值 %d（IP/UDP 头 + 18 字节协议头，含发送时间戳；否则时延/抖动无法统计）", i+1, f.Name, f.IPLen, minIPLen)
		}
		if f.IPLen > 65535 {
			return fmt.Errorf("flow %d (%s): ip_len %d 超过上限 65535", i+1, f.Name, f.IPLen)
		}
	}
	return nil
}

// LoadRecv 读取接收端配置：与 Load 相同但跳过速率与包长校验。
func LoadRecv(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析配置 %s: %w", path, err)
	}
	if err := cfg.ValidateRecv(); err != nil {
		return nil, fmt.Errorf("配置校验失败: %w", err)
	}
	return &cfg, nil
}
