package sources

import (
	"encoding/json"
	"testing"
)

func mustMatrix(t *testing.T, s string) *metricsMatrix {
	t.Helper()
	var m metricsMatrix
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return &m
}

func TestCPUUsedPercent(t *testing.T) {
	// Two modes over a window. idle grows by 20, non-idle (user) by 80 → total
	// Δ100, used% = 100*(1 - 20/100) = 80.
	m := mustMatrix(t, `{"data":{"result":[
		{"metric":{"mode":"idle"},"values":[[100,"1000"],[160,"1020"]]},
		{"metric":{"mode":"user"},"values":[[100,"500"],[160,"580"]]}
	]}}`)
	pct, ok := cpuUsedPercent(m)
	if !ok {
		t.Fatal("expected ok")
	}
	if pct < 79.9 || pct > 80.1 {
		t.Fatalf("cpu%% = %.2f, want ~80", pct)
	}
}

func TestCPUUsedPercentNoData(t *testing.T) {
	m := mustMatrix(t, `{"data":{"result":[]}}`)
	if _, ok := cpuUsedPercent(m); ok {
		t.Fatal("empty result must yield ok=false (agent not installed)")
	}
}

func TestMemUsedPercent(t *testing.T) {
	total := mustMatrix(t, `{"data":{"result":[{"metric":{},"values":[[100,"1000000000"],[160,"1000000000"]]}]}}`)
	avail := mustMatrix(t, `{"data":{"result":[{"metric":{},"values":[[100,"300000000"],[160,"250000000"]]}]}}`)
	pct, ok := memUsedPercent(total, avail)
	if !ok {
		t.Fatal("expected ok")
	}
	// used = (1e9 - 2.5e8)/1e9 = 75%
	if pct < 74.9 || pct > 75.1 {
		t.Fatalf("mem%% = %.2f, want ~75", pct)
	}
}

func TestLoadTransition(t *testing.T) {
	d := NewDigitalOceanLoad("tok", 85, 90, 0, testDeps()).(*doLoadSource)
	now := billingNow

	// First sample healthy → silent.
	if _, ok := d.transition("111", "web-1", "cpu", 40, 85, now); ok {
		t.Fatal("healthy first sample should not alert")
	}
	// Crosses threshold → HIGH alert.
	a, ok := d.transition("111", "web-1", "cpu", 92, 85, now)
	if !ok || a.Importance.String() != "high" {
		t.Fatalf("over-threshold should alert high, got ok=%v imp=%v", ok, a.Importance)
	}
	// Still over → no repeat.
	if _, ok := d.transition("111", "web-1", "cpu", 95, 85, now); ok {
		t.Fatal("no repeat while still over threshold")
	}
	// Back to normal → recovery.
	a2, ok := d.transition("111", "web-1", "cpu", 50, 85, now)
	if !ok || a2.MatchedRules[0] != "load:recovered" {
		t.Fatalf("expected recovery alert, got %+v", a2)
	}
}

func TestLoadDistinctIDsSameNameDoNotCollide(t *testing.T) {
	d := NewDigitalOceanLoad("tok", 85, 90, 0, testDeps()).(*doLoadSource)
	now := billingNow
	// Two droplets both named "web" but with distinct IDs.
	a1, ok1 := d.transition("111", "web", "cpu", 95, 85, now) // over
	if !ok1 || a1.Importance.String() != "high" {
		t.Fatalf("droplet 111 should alert high")
	}
	// Droplet 222's first (healthy) sample must NOT emit a false recovery for
	// 111's over-state — they must occupy separate state slots.
	if _, ok2 := d.transition("222", "web", "cpu", 10, 85, now); ok2 {
		t.Fatal("distinct-ID droplet must not clobber another's state (false recovery)")
	}
	// 111 still over → no repeat, its state was untouched.
	if _, ok := d.transition("111", "web", "cpu", 96, 85, now); ok {
		t.Fatal("111 must remain in over-state (no flapping)")
	}
}
