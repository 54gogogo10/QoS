package web

import (
	"crypto/subtle"
	"net"
	"net/http"
	"sync"
	"time"
)

// 控制端点限流参数（v2.10.0）：页面正常操作（启停/改配置/导报告）远低于该阈值；
// 目的是给无认证控制面加上暴力请求成本，而非精确计量。
const (
	ctrlRateLimit  = 30               // 每 IP 每窗口允许的控制类请求数
	ctrlRateWindow = 10 * time.Second // 固定窗口时长
	// rateTabMax 限流表容量上限：防伪造源 IP（代理/直连均可伪造 UDP 场景外的
	// TCP 源地址受限，但代理链可能呈现大量地址）灌爆内存。超限时先清过期项。
	rateTabMax = 8192
)

// ctrlRateEntry 是单 IP 的固定窗口计数。
type ctrlRateEntry struct {
	count int
	start time.Time
}

// ctrlRateLimiter 控制类端点每 IP 固定窗口限流（v2.10.0）。
// 只作用于"控制类"POST 端点（启停/配置/调速/报告/通知）；读端点不限流
// （页面每 500ms 轮询 /api/stats，多标签页下不应被拒）。
type ctrlRateLimiter struct {
	mu      sync.Mutex
	entries map[string]*ctrlRateEntry
	limit   int
	window  time.Duration
}

func newCtrlRateLimiter(limit int, window time.Duration) *ctrlRateLimiter {
	return &ctrlRateLimiter{
		entries: map[string]*ctrlRateEntry{},
		limit:   limit,
		window:  window,
	}
}

// allow 记录该 IP 的一次请求并判断本窗口是否仍被允许；超限返回 false。
func (l *ctrlRateLimiter) allow(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.entries) >= rateTabMax {
		for k, e := range l.entries {
			if now.Sub(e.start) >= l.window {
				delete(l.entries, k)
			}
		}
		// 仍满：说明单窗口内出现了超量源地址（异常/滥用），整表重置保内存上界。
		// 代价是攻击窗口内限流计数归零，可接受（内存安全优先于计数精度）。
		if len(l.entries) >= rateTabMax {
			l.entries = map[string]*ctrlRateEntry{}
		}
	}
	e := l.entries[ip]
	if e == nil || now.Sub(e.start) >= l.window {
		l.entries[ip] = &ctrlRateEntry{count: 1, start: now}
		return true
	}
	e.count++
	return e.count <= l.limit
}

// checkControlGate 控制类端点公共门禁（v2.10.0），按序：
// ① 每 IP 限流（含 remote 机器间通知——它们同样消耗控制面资源）；
// ② 配置了 token 时校验 X-Auth-Token（常数时间比较，防时序侧信道）。
// 读端点（stats/status/interfaces/tx_stats/time/receivers/metrics）不走此门禁。
// 返回 false 表示已写出错误响应。
func (s *Server) checkControlGate(w http.ResponseWriter, r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if !s.rateLimiter.allow(host, time.Now()) {
		writeJSONError(w, http.StatusTooManyRequests, "控制请求过于频繁，请稍后再试")
		return false
	}
	if s.token != "" &&
		constantTimeEq(r.Header.Get("X-Auth-Token"), s.token) != 1 {
		writeJSONError(w, http.StatusUnauthorized, "缺少或错误的 X-Auth-Token 请求头")
		return false
	}
	return true
}

// constantTimeEq 常数时间字符串比较（token 校验防时序侧信道）。
func constantTimeEq(a, b string) int {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b))
}
