// Package sender 实现 8 条流的 UDP 发送引擎。
package sender

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"time"

	"qostool/internal/config"
	"qostool/internal/protocol"
	"qostool/internal/stats"
)

const defaultTick = 10 * time.Millisecond

// Sender 管理全部流的发送 goroutine。
type Sender struct {
	flows []*flow
	agg   *stats.Aggregator
}

// New 创建发送器；每条流一个 UDP socket。
func New(cfg *config.Config, agg *stats.Aggregator) (*Sender, error) {
	s := &Sender{agg: agg}
	for i, f := range cfg.Flows {
		fw, err := newFlow(i, f, agg)
		if err != nil {
			return nil, fmt.Errorf("flow %d (%s): %w", i+1, f.Name, err)
		}
		s.flows = append(s.flows, fw)
	}
	return s, nil
}

// Run 启动全部流 goroutine，直到 ctx 取消。
func (s *Sender) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, f := range s.flows {
		wg.Add(1)
		go func(f *flow) {
			defer wg.Done()
			f.run(ctx)
		}(f)
	}
	wg.Wait()
}

// Close 关闭全部流的 UDP socket（用于启动失败时的清理）。
func (s *Sender) Close() {
	for _, f := range s.flows {
		f.conn.Close()
	}
}

type flow struct {
	idx          int
	conn         *net.UDPConn
	bucket       *bucket
	payload      []byte
	seq          uint32
	agg          *stats.Aggregator
	wireOverhead int
}

func newFlow(idx int, cfg config.Flow, agg *stats.Aggregator) (*flow, error) {
	laddr := &net.UDPAddr{IP: net.ParseIP(cfg.SrcIP), Port: cfg.SrcPort}
	raddr := &net.UDPAddr{IP: net.ParseIP(cfg.DstIP), Port: cfg.DstPort}
	conn, err := net.DialUDP("udp", laddr, raddr)
	if err != nil {
		return nil, err
	}
	if err := setDSCP(conn, int(cfg.DSCP)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("设置 DSCP=%d 失败: %w", cfg.DSCP, err)
	}
	payload := make([]byte, protocol.HeaderSize+cfg.PayloadSize)
	protocol.EncodeHeader(payload, uint16(idx), 0)
	wireOverhead := 28 // IPv4: 20 IP + 8 UDP
	if net.ParseIP(cfg.SrcIP).To4() == nil {
		wireOverhead = 48 // IPv6: 40 IP + 8 UDP
	}
	return &flow{
		idx:          idx,
		conn:         conn,
		bucket:       newBucket(cfg.RateMbps, cfg.RatePPS, defaultTick),
		payload:      payload,
		agg:          agg,
		wireOverhead: wireOverhead,
	}, nil
}

// run 按绝对时间点调度：每 10ms 批量发送本轮配额。
func (f *flow) run(ctx context.Context) {
	next := time.Now()
	for {
		next = next.Add(defaultTick)
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			f.conn.Close()
			return
		case <-timer.C:
		}
		n := f.bucket.take(time.Now(), len(f.payload))
		for i := 0; i < n; i++ {
			f.seq++
			binary.BigEndian.PutUint32(f.payload[protocol.HeaderSize-4:protocol.HeaderSize], f.seq)
			if _, err := f.conn.Write(f.payload); err != nil {
				return // 对端不可达等错误：停止该流
			}
		}
		if n > 0 {
			f.agg.RecordTx(f.idx, uint64(n), uint64(n*(len(f.payload)+f.wireOverhead)))
		}
	}
}
