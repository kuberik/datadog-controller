package controller

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kuberikrolloutv1alpha1 "github.com/kuberik/rollout-controller/api/v1alpha1"
	obs "github.com/kuberik/rollout-controller/pkg/healthcheck/observation"
)

func TestParseMonitorQuery(t *testing.T) {
	cases := []struct {
		query      string
		ok         bool
		agg, win   string
		expr, cmp  string
		threshold  float64
		wantDirect kuberikrolloutv1alpha1.HealthCheckObservationDirection
	}{
		{"avg(last_5m):avg:system.cpu.user{*} by {host} > 80", true, "avg", "5m", "avg:system.cpu.user{*} by {host}", ">", 80, "Above"},
		{"max(last_1h):max:lat{env:prod} >= 0.5", true, "max", "1h", "max:lat{env:prod}", ">=", 0.5, "Above"},
		{"min(last_15m):sum:req{*}.as_count() < 100", true, "min", "15m", "sum:req{*}.as_count()", "<", 100, "Below"},
		{"sum(last_1d):a{*} / b{*} <= -2", true, "sum", "1d", "a{*} / b{*}", "<=", -2, "Below"},
		{"avg(last_5m):a{*} == 1", true, "avg", "5m", "a{*}", "==", 1, ""},
		{"change(avg(last_5m)):avg:a{*} > 50", false, "", "", "", "", 0, ""},
		{`"http.can_connect".over("*").last(2).count_by_status()`, false, "", "", "", "", 0, ""},
		{"avg(last_5m):avg:a{*}", false, "", "", "", "", 0, ""},
	}
	for _, c := range cases {
		got, ok := parseMonitorQuery(c.query)
		if ok != c.ok {
			t.Fatalf("%q: ok=%v want %v", c.query, ok, c.ok)
		}
		if !ok {
			continue
		}
		if got.Aggregator != c.agg || got.Window != c.win || got.Expression != c.expr || got.Comparator != c.cmp || got.Threshold != c.threshold {
			t.Errorf("%q: got %+v", c.query, got)
		}
		if d, _ := comparatorDirection(got.Comparator); d != c.wantDirect {
			t.Errorf("%q: direction %q want %q", c.query, d, c.wantDirect)
		}
	}
}

func TestParseWindow(t *testing.T) {
	for in, want := range map[string]string{"last_5m": "5m", "5m": "5m", "last_1d": "1d", "last_1w": "1w", "last_2mo": "2mo", "last_x": "", "": "", "5": ""} {
		if got := parseWindow(in); got != want {
			t.Errorf("parseWindow(%q)=%q want %q", in, got, want)
		}
	}
	if d, ok := windowDuration("2h"); !ok || d != 2*time.Hour {
		t.Errorf("windowDuration(2h)=%v,%v", d, ok)
	}
	if _, ok := windowDuration("0m"); ok {
		t.Error("0m must be rejected")
	}
}

type stubQuerier struct{ series []metricSeries }

func (s stubQuerier) QueryMetric(context.Context, string, time.Duration) ([]metricSeries, error) {
	return s.series, nil
}

func TestMonitorObservation(t *testing.T) {
	now := metav1.Now()
	yes := true
	crit := 90.0
	facts := monitorFacts{Name: "cpu", Type: "metric alert", Query: "max(last_5m):avg:cpu{*} by {host} > 80", Critical: &crit, Healthy: &yes}
	q := stubQuerier{series: []metricSeries{
		{Tags: []string{"host:a"}, Points: []float64{10, 20}},
		{Tags: []string{"host:b"}, Points: []float64{70, 85.5}},
	}}
	got := monitorObservation(context.Background(), q, facts, nil, now)
	if got.Kind != kuberikrolloutv1alpha1.ObservationKindThreshold || got.Value != "85.5" || got.Limit != "90" ||
		got.Direction != kuberikrolloutv1alpha1.ObservationDirectionAbove || got.Window != "5m" || got.Unit != "" {
		t.Errorf("unexpected threshold observation: %+v", got)
	}
	// group tags narrow the series; Below picks the lowest.
	facts.Query = "min(last_5m):avg:cpu{*} by {host} < 5"
	got = monitorObservation(context.Background(), q, facts, []string{"host:a"}, now)
	if got.Value != "10" || got.Direction != kuberikrolloutv1alpha1.ObservationDirectionBelow {
		t.Errorf("group-tag/below observation: %+v", got)
	}
	// no series or no querier -> State; unsupported type -> State.
	for _, state := range []observation{
		monitorObservation(context.Background(), stubQuerier{}, facts, nil, now),
		monitorObservation(context.Background(), nil, facts, nil, now),
		monitorObservation(context.Background(), q, monitorFacts{Name: "x", Type: "composite", Query: "1 && 2"}, nil, now),
		monitorObservation(context.Background(), q, monitorFacts{Name: "x", Type: "metric alert", Query: "avg(last_5m):a{*} == 3"}, nil, now),
	} {
		if state.Kind != kuberikrolloutv1alpha1.ObservationKindState || state.Value != "" || state.Limit != "" || state.Name == "" {
			t.Errorf("expected State, got %+v", state)
		}
	}
}

func TestIncidentsObservation(t *testing.T) {
	now := metav1.Now()
	none, some := obs.Count(incidentsObservationName, 0, now), obs.Count(incidentsObservationName, 3, now)
	if none.Name != "open incidents" || none.Kind != kuberikrolloutv1alpha1.ObservationKindCount || none.Value != "0" || !*none.Healthy {
		t.Errorf("zero incidents: %+v", none)
	}
	if some.Value != "3" || *some.Healthy {
		t.Errorf("3 incidents: %+v", some)
	}
}
