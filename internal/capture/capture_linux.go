//go:build linux

package capture

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"time"

	"github.com/gopacket/gopacket/layers"
	"golang.org/x/sys/unix"

	"qostool/internal/config"
	"qostool/internal/protocol"
	"qostool/internal/stats"
)

// Linux 纯 Go 抓包实现：AF_PACKET 原始套接字（用 x/sys/unix）。
// 不依赖 libpcap/cgo，支持 GOOS=linux 任意架构交叉编译（麒麟 V10 等）。
// 需要 root 权限（与 pcap 相同）。
// 注意：必须用 x/sys/unix 而非标准库 syscall——实测标准库 syscall 的
// AF_PACKET 在部分环境（如 WSL2）收不到帧，x/sys/unix 正常。

const ethPAll = 0x0003 // ETH_P_ALL：接收所有协议

// Capturer 抓取接口上的 IP 包并喂给统计聚合器。
type Capturer struct {
	fd      int
	matcher *matcher
	agg     *stats.Aggregator
	iface   string

	totalPkts   atomic.Uint64 // 接口上抓到的 IP 包总数（含所有协议）
	matchedPkts atomic.Uint64 // 匹配配置且校验通过的包数
	otherPkts   atomic.Uint64 // 抓到但未匹配/无 magic 的包数
}

// IfaceStats 是接口级抓包统计（诊断"网卡有流量但 RX 为 0"用）。
type IfaceStats struct {
	TotalPkts   uint64 `json:"total_pkts"`
	MatchedPkts uint64 `json:"matched_pkts"`
	OtherPkts   uint64 `json:"other_pkts"`
}

// Stats 返回接口级抓包统计。
func (c *Capturer) Stats() IfaceStats {
	return IfaceStats{
		TotalPkts:   c.totalPkts.Load(),
		MatchedPkts: c.matchedPkts.Load(),
		OtherPkts:   c.otherPkts.Load(),
	}
}

// New 打开接口的 AF_PACKET 原始套接字。
func New(iface string, cfg *config.Config, agg *stats.Aggregator) (*Capturer, error) {
	ifi, err := net.InterfaceByName(iface)
	if err != nil {
		return nil, fmt.Errorf("打开接口 %q 失败: %w", iface, err)
	}
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW|unix.SOCK_CLOEXEC, int(htons(ethPAll)))
	if err != nil {
		return nil, fmt.Errorf("创建 AF_PACKET 套接字失败（需要 root 权限）: %w", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrLinklayer{
		Protocol: htons(ethPAll),
		Ifindex:  ifi.Index,
	}); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("绑定接口 %q 失败（需要 root 权限）: %w", iface, err)
	}
	return &Capturer{fd: fd, matcher: newMatcher(cfg), agg: agg, iface: iface}, nil
}

// Run 阻塞抓包直到 ctx 取消；接口故障时返回错误。
func (c *Capturer) Run(ctx context.Context) error {
	defer unix.Close(c.fd)
	buf := make([]byte, 65536)
	// 非阻塞 + 短轮询，保证 ctx 取消及时响应
	if err := unix.SetNonblock(c.fd, true); err != nil {
		return fmt.Errorf("设置非阻塞失败: %w", err)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		// 读空接收队列：每轮把已到达的帧全部处理掉（避免高 pps 堆积丢帧）
		for {
			n, _, err := unix.Recvfrom(c.fd, buf, 0)
			if err != nil {
				if err == unix.EAGAIN || err == unix.EWOULDBLOCK {
					break // 队列读空
				}
				if ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("接口 %s 抓包失败: %w", c.iface, err)
			}
			if n <= 0 {
				continue
			}
			frame := buf[:n]
			// 试探性链路解析：先按以太网帧（14 字节头）解析，
			// 结果不是 IP 包时回退为裸 IP 帧（tun 等无链路头接口）。
			// 注意：Linux lo 的 AF_PACKET 帧带 14 字节伪以太头（MAC 全 0），
			// 与物理网卡相同，因此统一先按以太网尝试。
			ip := skipLinkHeader(layers.LinkTypeEthernet, frame)
			if ip == nil || len(ip) < 1 || (ip[0]>>4 != 4 && ip[0]>>4 != 6) {
				ip = frame
			}
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
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(2 * time.Millisecond):
		}
	}
}

// htons 主机字节序 → 网络字节序。
func htons(v uint16) uint16 {
	return v<<8 | v>>8
}
