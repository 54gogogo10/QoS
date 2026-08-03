//go:build !linux

package capture

import (
	"context"
	"fmt"
	"runtime"
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
		idx := c.matcher.match(pkt.key)
		if idx < 0 {
			continue
		}
		fid, seq, ok := protocol.DecodeHeader(pkt.payload)
		if !ok || int(fid) != idx {
			continue // 非本工具流量或 flow_id 不一致
		}
		c.agg.RecordRx(idx, uint64(len(ip)), seq)
	}
}
