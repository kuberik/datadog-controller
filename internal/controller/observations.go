package controller

import (
	"context"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	kuberikrolloutv1alpha1 "github.com/kuberik/rollout-controller/api/v1alpha1"
)

type observation = kuberikrolloutv1alpha1.HealthCheckObservation

const (
	// maxObservations mirrors the CRD's MaxItems on status.observations.
	maxObservations = 16

	incidentsObservationName = "open incidents"
)

// metricSeries is one timeseries returned by a Datadog metric query.
type metricSeries struct {
	// Tags are the series' group tags, e.g. "service:api".
	Tags   []string
	Points []float64
	// Unit is the Datadog unit name of the series ("millisecond"), if any.
	Unit string
}

// metricQuerier is implemented by Datadog clients that can evaluate a metric
// query. Datadog's monitor API exposes states but not the value a monitor last
// evaluated, so the current value is recomputed from the monitor's own query.
type metricQuerier interface {
	QueryMetric(ctx context.Context, query string, window time.Duration) ([]metricSeries, error)
}

// monitorQuery is a parsed metric/query-alert monitor query such as
// "avg(last_5m):avg:system.cpu.user{*} by {host} > 80".
type monitorQuery struct {
	Aggregator string
	Window     string
	Expression string
	Comparator string
	Threshold  float64
}

var (
	monitorQueryPattern = regexp.MustCompile(
		`(?s)^\s*([a-z_]+)\(\s*last_(\d+[a-z]+)\s*\)\s*:\s*(.+?)\s*(>=|<=|==|!=|>|<)\s*(-?\d+(?:\.\d+)?(?:[eE][-+]?\d+)?)\s*$`)
	windowPattern = regexp.MustCompile(`^(\d+)(s|m|h|d|w|mo)$`)
)

// parseMonitorQuery splits a metric monitor query into its parts. ok is false
// for queries it does not understand (change/pct_change wrappers, formulas
// without a trailing comparator, other monitor types' query languages).
func parseMonitorQuery(query string) (monitorQuery, bool) {
	m := monitorQueryPattern.FindStringSubmatch(query)
	if m == nil {
		return monitorQuery{}, false
	}
	threshold, err := strconv.ParseFloat(m[5], 64)
	if err != nil {
		return monitorQuery{}, false
	}
	return monitorQuery{Aggregator: m[1], Window: parseWindow(m[2]), Expression: m[3], Comparator: m[4], Threshold: threshold}, true
}

// parseWindow turns a Datadog evaluation window ("5m", from "last_5m") into the
// observation window; it returns "" for anything unrecognised.
func parseWindow(window string) string {
	window = strings.TrimPrefix(strings.TrimSpace(window), "last_")
	if !windowPattern.MatchString(window) {
		return ""
	}
	return window
}

// windowDuration converts an observation window to a duration.
func windowDuration(window string) (time.Duration, bool) {
	m := windowPattern.FindStringSubmatch(window)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0, false
	}
	unit := map[string]time.Duration{
		"s": time.Second, "m": time.Minute, "h": time.Hour,
		"d": 24 * time.Hour, "w": 7 * 24 * time.Hour, "mo": 30 * 24 * time.Hour,
	}[m[2]]
	return time.Duration(n) * unit, true
}

// comparatorDirection maps a monitor comparator to the side of the limit that
// is bad. Equality comparators have no direction.
func comparatorDirection(comparator string) (kuberikrolloutv1alpha1.HealthCheckObservationDirection, bool) {
	switch comparator {
	case ">", ">=":
		return kuberikrolloutv1alpha1.ObservationDirectionAbove, true
	case "<", "<=":
		return kuberikrolloutv1alpha1.ObservationDirectionBelow, true
	}
	return "", false
}

// formatDecimal renders v as a plain decimal matching the CRD pattern
// ^-?[0-9]+(\.[0-9]+)?$: no exponent, no suffix. ok is false for NaN/Inf.
func formatDecimal(v float64) (string, bool) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "", false
	}
	s := strconv.FormatFloat(v, 'f', -1, 64)
	if s == "-0" {
		s = "0"
	}
	return s, true
}

// aggregate applies a monitor time aggregation to a series' points.
func aggregate(aggregator string, points []float64) (float64, bool) {
	if len(points) == 0 {
		return 0, false
	}
	switch aggregator {
	case "avg":
		sum := 0.0
		for _, p := range points {
			sum += p
		}
		return sum / float64(len(points)), true
	case "sum":
		sum := 0.0
		for _, p := range points {
			sum += p
		}
		return sum, true
	case "min":
		m := points[0]
		for _, p := range points {
			m = math.Min(m, p)
		}
		return m, true
	case "max":
		m := points[0]
		for _, p := range points {
			m = math.Max(m, p)
		}
		return m, true
	case "last":
		return points[len(points)-1], true
	}
	return 0, false
}

func unitFromDatadog(name string) string {
	switch name {
	case "millisecond":
		return "Milliseconds"
	case "second":
		return "Seconds"
	}
	// Datadog percentages are 0..100, not the 0..1 ratio the Percent unit means.
	return ""
}

// monitorFacts is what an observation is built from, whether it came from the
// Datadog API or a DatadogMonitor resource.
type monitorFacts struct {
	Name  string
	Type  string
	Query string
	// Critical is options.thresholds.critical, if set.
	Critical *float64
	// Healthy is nil when the monitor has no verdict (no data, skipped, ...).
	Healthy *bool
}

func isMetricMonitorType(t string) bool { return t == "metric alert" || t == "query alert" }

// currentMetricValue evaluates the monitor's query over its window and returns
// the aggregated value of the series closest to breaching the limit, among the
// series consistent with the group tags.
func currentMetricValue(ctx context.Context, q metricQuerier, parsed monitorQuery, direction kuberikrolloutv1alpha1.HealthCheckObservationDirection, groupTags []string) (float64, string, bool) {
	window, ok := windowDuration(parsed.Window)
	if !ok || q == nil {
		return 0, "", false
	}
	series, err := q.QueryMetric(ctx, parsed.Expression, window)
	if err != nil {
		return 0, "", false
	}
	var (
		best  float64
		unit  string
		found bool
	)
	for _, s := range series {
		if !groupMatches(strings.Join(s.Tags, ","), groupTags) {
			continue
		}
		v, ok := aggregate(parsed.Aggregator, s.Points)
		if !ok {
			continue
		}
		worse := !found ||
			(direction == kuberikrolloutv1alpha1.ObservationDirectionAbove && v > best) ||
			(direction == kuberikrolloutv1alpha1.ObservationDirectionBelow && v < best)
		if worse {
			best, unit, found = v, unitFromDatadog(s.Unit), true
		}
	}
	return best, unit, found
}

// monitorObservation builds the observation for one monitor: a Threshold when
// the monitor is a metric/query alert with a parseable limit and a current
// value, a State otherwise.
func monitorObservation(ctx context.Context, q metricQuerier, m monitorFacts, groupTags []string, now metav1.Time) observation {
	state := observation{
		Name:         m.Name,
		Kind:         kuberikrolloutv1alpha1.ObservationKindState,
		Healthy:      m.Healthy,
		ObservedTime: &now,
	}
	if !isMetricMonitorType(m.Type) {
		return state
	}
	parsed, ok := parseMonitorQuery(m.Query)
	if !ok {
		return state
	}
	direction, ok := comparatorDirection(parsed.Comparator)
	if !ok {
		return state
	}
	limit := parsed.Threshold
	if m.Critical != nil {
		limit = *m.Critical
	}
	limitStr, ok := formatDecimal(limit)
	if !ok {
		return state
	}
	value, unit, ok := currentMetricValue(ctx, q, parsed, direction, groupTags)
	if !ok {
		return state
	}
	valueStr, ok := formatDecimal(value)
	if !ok {
		return state
	}
	return observation{
		Name:         m.Name,
		Kind:         kuberikrolloutv1alpha1.ObservationKindThreshold,
		Value:        valueStr,
		Limit:        limitStr,
		Direction:    direction,
		Unit:         unit,
		Window:       parsed.Window,
		Healthy:      m.Healthy,
		ObservedTime: &now,
	}
}

// incidentsObservation is the Count of open incidents; healthy while there are none.
func incidentsObservation(open int, now metav1.Time) observation {
	healthy := open == 0
	return observation{
		Name:         incidentsObservationName,
		Kind:         kuberikrolloutv1alpha1.ObservationKindCount,
		Value:        strconv.Itoa(open),
		Healthy:      &healthy,
		ObservedTime: &now,
	}
}

// healthFromStatuses is false when any status is Alert, true when all are OK,
// and nil (no verdict) otherwise.
func healthFromStatuses(statuses []string) *bool {
	allOK := len(statuses) > 0
	for _, s := range statuses {
		if s == monitorStatusAlert {
			f := false
			return &f
		}
		if s != "OK" {
			allOK = false
		}
	}
	if allOK {
		t := true
		return &t
	}
	return nil
}

// sortObservations puts failing observations first, then by name, and caps the list.
func sortObservations(obs []observation) []observation {
	rank := func(o observation) int {
		switch {
		case o.Healthy != nil && !*o.Healthy:
			return 0
		case o.Healthy == nil:
			return 1
		}
		return 2
	}
	sort.SliceStable(obs, func(i, j int) bool {
		if ri, rj := rank(obs[i]), rank(obs[j]); ri != rj {
			return ri < rj
		}
		return obs[i].Name < obs[j].Name
	})
	if len(obs) > maxObservations {
		obs = obs[:maxObservations]
	}
	return obs
}

// mergeObservations keeps the previous ObservedTime of observations that did not
// change, so a steady value does not rewrite status (and wake watchers) on
// every poll. It reports whether the list differs from existing.
func mergeObservations(existing, updated []observation) ([]observation, bool) {
	merged := make([]observation, len(updated))
	changed := len(existing) != len(updated)
	for i, o := range updated {
		merged[i] = o
		if i < len(existing) {
			prev, next := existing[i], o
			prev.ObservedTime, next.ObservedTime = nil, nil
			if equality.Semantic.DeepEqual(prev, next) && existing[i].ObservedTime != nil {
				merged[i].ObservedTime = existing[i].ObservedTime
				continue
			}
		}
		changed = true
	}
	return merged, changed
}

// ignoreObservationOnlyUpdates drops HealthCheck update events whose only change
// is status.observations, which this controller writes itself; a drifting metric
// value would otherwise retrigger a reconcile (and a Datadog query) on every poll.
func ignoreObservationOnlyUpdates() predicate.Predicate {
	return predicate.Funcs{UpdateFunc: func(e event.UpdateEvent) bool {
		oldHC, ok1 := e.ObjectOld.(*kuberikrolloutv1alpha1.HealthCheck)
		newHC, ok2 := e.ObjectNew.(*kuberikrolloutv1alpha1.HealthCheck)
		if !ok1 || !ok2 || equality.Semantic.DeepEqual(oldHC.Status.Observations, newHC.Status.Observations) {
			return true
		}
		oldStatus, newStatus := oldHC.Status.DeepCopy(), newHC.Status.DeepCopy()
		oldStatus.Observations, newStatus.Observations = nil, nil
		return !(oldHC.Generation == newHC.Generation &&
			equality.Semantic.DeepEqual(oldHC.Labels, newHC.Labels) &&
			equality.Semantic.DeepEqual(oldHC.Annotations, newHC.Annotations) &&
			equality.Semantic.DeepEqual(oldStatus, newStatus))
	}}
}
