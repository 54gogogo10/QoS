package web

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"qostool/internal/config"
	"qostool/internal/stats"
)

func newTestServer() *Server {
	cfg := &config.Config{Flows: []config.Flow{
		{Name: "ef", SrcIP: "1.1.1.1", DstIP: "2.2.2.2", SrcPort: 100, DstPort: 200, DSCP: 46},
	}}
	agg := stats.NewAggregator(1, 10)
	agg.RecordTx(0, 100, 1000)
	agg.RecordRx(0, 500, 1)
	agg.RecordRx(0, 500, 5)
	agg.Snapshot(time.Now())
	return New(cfg, agg)
}

func TestAPIStats(t *testing.T) {
	s := newTestServer()
	rec := httptest.NewRecorder()
	s.handleStats(rec, httptest.NewRequest("GET", "/api/stats", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	var out apiStats
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Flows) != 1 {
		t.Fatalf("flows = %d", len(out.Flows))
	}
	f := out.Flows[0]
	if f.Name != "ef" || f.DSCP != 46 || f.SrcPort != 100 || f.DstPort != 200 ||
		f.TxPackets != 100 || f.TxBytes != 1000 || f.DstIP != "2.2.2.2" ||
		f.RxPackets != 2 || f.RxBytes != 1000 || f.Lost != 3 ||
		math.Abs(f.LossRate-0.6) >= 1e-9 {
		t.Fatalf("flow 字段错误: %+v", f)
	}
	if len(out.History.T) != 1 || len(out.History.Tx) != 1 || len(out.History.Tx[0]) != 1 {
		t.Fatalf("history 错误: %+v", out.History)
	}
}

func TestIndexPage(t *testing.T) {
	s := newTestServer()
	rec := httptest.NewRecorder()
	s.handleIndex(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "canvas") || !strings.Contains(body, "api/stats") {
		t.Fatal("页面缺少 canvas 或 api/stats 引用")
	}
}
