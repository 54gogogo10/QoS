//go:build !linux

package capture

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gopacket/gopacket/pcap"

	"qostool/internal/config"
	"qostool/internal/protocol"
	"qostool/internal/stats"
)

// Capturer 抓取接口上的 IP 包并喂给统计聚合器。
type Capturer struct {
	handle  *pcap.Handle
	matcher *matcher
	agg     *stats.Aggregator
	iface   string

	totalPkts   atomic.Uint64 // 接口上抓到的 IP 包总数（含所有协议）
	matchedPkts atomic.Uint64 // 匹配配置且校验通过的包数
	otherPkts   atomic.Uint64 // 抓到但未匹配/无 magic 的包数
}

// IfaceStats 是接口级抓包统计（诊断"网卡有流量但 RX 为 0"用）。
type IfaceStats struct {
	TotalPkts   uint64 `json:"total_pkts"`   // 接口上抓到的 IP 包总数
	MatchedPkts uint64 `json:"matched_pkts"` // 匹配配置并计入 RX 的包数
	OtherPkts   uint64 `json:"other_pkts"`   // 未匹配/非本工具流量
}

// Iface 返回接口名（错误日志用）。
func (c *Capturer) Iface() string {
	return c.iface
}

// Stats 返回接口级抓包统计。
func (c *Capturer) Stats() IfaceStats {
	return IfaceStats{
		TotalPkts:   c.totalPkts.Load(),
		MatchedPkts: c.matchedPkts.Load(),
		OtherPkts:   c.otherPkts.Load(),
	}
}

// captureBufferSize 是 pcap 内核缓冲大小（64MB）。
// Npcap 默认缓冲很小（约 1MB），高 pps 时抓包丢帧会造成 RX < TX 的假象。
const captureBufferSize = 64 << 20

// New 打开接口（Linux libpcap / Windows Npcap），snaplen 65535，混杂模式。
func New(iface string, cfg *config.Config, agg *stats.Aggregator) (*Capturer, error) {
	// 用 InactiveHandle 以便在激活前设置内核缓冲（OpenLive 激活后无法再改）
	inactive, err := pcap.NewInactiveHandle(iface)
	if err != nil {
		if runtime.GOOS == "windows" {
			return nil, fmt.Errorf("打开接口 %q 失败（Windows 需安装 Npcap: https://npcap.com）: %w", iface, err)
		}
		return nil, fmt.Errorf("打开接口 %q 失败: %w", iface, err)
	}
	defer inactive.CleanUp()
	inactive.SetSnapLen(65535)
	inactive.SetPromisc(true)
	inactive.SetTimeout(100 * time.Millisecond)
	if err := inactive.SetBufferSize(captureBufferSize); err != nil {
		inactive.CleanUp()
		return nil, fmt.Errorf("设置抓包缓冲失败: %w", err)
	}
	handle, err := inactive.Activate()
	if err != nil {
		if runtime.GOOS == "windows" {
			return nil, fmt.Errorf("打开接口 %q 失败（Windows 需安装 Npcap: https://npcap.com）: %w", iface, err)
		}
		return nil, fmt.Errorf("打开接口 %q 失败: %w", iface, err)
	}
	if err := handle.SetBPFFilter("ip or ip6"); err != nil {
		handle.Close()
		return nil, fmt.Errorf("设置 BPF 过滤失败: %w", err)
	}
	return &Capturer{handle: handle, matcher: newMatcher(cfg), agg: agg, iface: iface}, nil
}

// Run 阻塞抓包直到 ctx 取消；接口故障时返回错误。
func (c *Capturer) Run(ctx context.Context) error {
	defer c.handle.Close()
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		data, _, err := c.handle.ReadPacketData()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			// Npcap 读超时（100ms 无流量）返回 "Timeout Expired"——这是正常情况，
			// 不是致命错误：继续循环等待后续流量（否则空窗期抓包线程会退出）。
			if strings.Contains(err.Error(), "Timeout") {
				continue
			}
			return fmt.Errorf("接口 %s 抓包失败: %w", c.iface, err)
		}
		if data == nil { // pcap 读超时
			continue
		}
		ip := skipLinkHeader(c.handle.LinkType(), data)
		if ip == nil {
			continue
		}
		pkt, err := parsePacket(ip)
		if err != nil {
			continue
		}
		c.totalPkts.Add(1)
		idx := c.matcher.match(pkt.key)
		if idx < 0 {
			c.otherPkts.Add(1)
			continue
		}
		fid, seq, ok := protocol.DecodeHeader(pkt.payload)
		if !ok || int(fid) != idx {
			c.otherPkts.Add(1) // 非本工具流量或 flow_id 不一致
			continue
		}
		c.matchedPkts.Add(1)
		c.agg.RecordRx(idx, uint64(len(ip)), seq)
	}
}
