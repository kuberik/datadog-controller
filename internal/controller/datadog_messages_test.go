package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kuberikrolloutv1alpha1 "github.com/kuberik/rollout-controller/api/v1alpha1"
)

func TestListMessage(t *testing.T) {
	if got := listMessage(nil, ", "); got != "" {
		t.Errorf("empty list: %q", got)
	}
	if got := listMessage([]string{"service:checkout", "env:prod"}, "; "); got != "service:checkout; env:prod" {
		t.Errorf("short list: %q", got)
	}
	items := make([]string, 0, 100)
	for i := 0; i < 100; i++ {
		items = append(items, fmt.Sprintf("service:svc-%03d", i))
	}
	got := listMessage(items, "; ")
	if len(got) > 256 {
		t.Fatalf("%d bytes, want <= 256", len(got))
	}
	if !strings.HasPrefix(got, "service:svc-000; service:svc-001") || !strings.Contains(got, " and ") || !strings.HasSuffix(got, " more") {
		t.Errorf("long list: %q", got)
	}
	var kept, more int
	if _, err := fmt.Sscanf(got[strings.LastIndex(got, " and ")+5:], "%d more", &more); err != nil {
		t.Fatal(err)
	}
	kept = strings.Count(got, "service:")
	if kept+more != 100 {
		t.Errorf("kept %d + more %d != 100", kept, more)
	}
}

func TestListMessageNormalisesAndNeverReturnsBareCount(t *testing.T) {
	long := strings.Repeat("x", 300)
	cases := []struct {
		name  string
		items []string
		check func(string) bool
		want  string
	}{
		{name: "double space", items: []string{"#1 a  b", "#2 c"}, want: "#1 a b, #2 c"},
		{name: "newline", items: []string{"#1 a\nb", "#2 c"}, want: "#1 a b, #2 c"},
		{name: "single item over cap", items: []string{long}, check: func(g string) bool {
			return len(g) <= 256 && strings.HasPrefix(g, "xxx") && strings.HasSuffix(g, "…")
		}},
		{name: "long first then b", items: []string{long, "b"}, check: func(g string) bool {
			return len(g) <= 256 && strings.HasPrefix(g, "xxx") && strings.HasSuffix(g, "…") && !strings.Contains(g, "more")
		}},
		{name: "multibyte first", items: []string{strings.Repeat("é", 200), "b"}, check: func(g string) bool {
			return len(g) <= 256 && utf8.ValidString(g) && strings.HasSuffix(g, "…")
		}},
	}
	for _, c := range cases {
		got := listMessage(c.items, ", ")
		if c.check != nil && !c.check(got) {
			t.Errorf("%s: %q", c.name, got)
		}
		if c.check == nil && got != c.want {
			t.Errorf("%s: %q want %q", c.name, got, c.want)
		}
	}
}

func TestGroupsMessageSkipsUngroupedAndSorts(t *testing.T) {
	if got := groupsMessage([]string{"*"}); got != "" {
		t.Errorf("ungrouped monitor: %q", got)
	}
	if got := groupsMessage([]string{"env:prod,service:payments", "env:prod,service:api"}); got != "env:prod,service:api; env:prod,service:payments" {
		t.Errorf("got %q", got)
	}
}

// erroringAPI fails every call with err.
type erroringAPI struct{ err error }

func (e erroringAPI) GetMonitor(context.Context, int64) (datadogMonitorState, error) {
	return datadogMonitorState{}, e.err
}
func (e erroringAPI) ListMonitorsByTags(context.Context, []string) ([]datadogMonitorState, error) {
	return nil, e.err
}
func (e erroringAPI) SearchOpenIncidents(context.Context, string) ([]datadogIncident, error) {
	return nil, e.err
}

var _ = Describe("Datadog observation messages", func() {
	var namespace string

	BeforeEach(func() {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "datadog-msg-"}}
		Expect(k8sClient.Create(context.Background(), ns)).To(Succeed())
		namespace = ns.Name
	})

	reconcileWith := func(api datadogAPI, name, class string, annotations map[string]string) *kuberikrolloutv1alpha1.HealthCheck {
		hc := &kuberikrolloutv1alpha1.HealthCheck{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Annotations: annotations},
			Spec:       kuberikrolloutv1alpha1.HealthCheckSpec{Class: &class},
		}
		Expect(k8sClient.Create(context.Background(), hc)).To(Succeed())
		r := &HealthCheckReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), DatadogAPI: api}
		_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(hc)})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(hc), hc)).To(Succeed())
		return hc
	}

	It("names the alerting groups on a monitor's observation and nothing for OK or ungrouped monitors", func() {
		api := &fakeDatadogAPI{monitors: map[int64]datadogMonitorState{
			1: {ID: 1, Name: "checkout latency", OverallState: "Alert", Groups: map[string]string{
				"env:prod,service:payments": "Alert", "env:prod,service:checkout": "Alert", "env:prod,service:search": "OK", "env:prod,service:legacy": "Warn"}},
			2: {ID: 2, Name: "healthy", OverallState: "OK", Groups: map[string]string{"service:a": "OK"}},
			3: {ID: 3, Name: "ungrouped", OverallState: "Alert"},
		}}
		hc := reconcileWith(api, "groups", HealthCheckClassDatadog, map[string]string{AnnotationDatadogMonitorIDs: "1,2,3"})
		messages := map[string]string{}
		for _, o := range hc.Status.Observations {
			messages[o.Name] = o.Message
		}
		Expect(messages).To(Equal(map[string]string{
			"checkout latency": "env:prod,service:checkout; env:prod,service:payments",
			"healthy":          "",
			"ungrouped":        "",
		}))
	})

	It("lists the open incidents on the Count observation", func() {
		api := &fakeDatadogAPI{incidents: []datadogIncident{{PublicID: 412, Title: "Checkout errors"}, {PublicID: 415, Title: "Payment timeouts"}}}
		hc := reconcileWith(api, "incidents", HealthCheckClassDatadogIncidents, map[string]string{AnnotationDatadogIncidentQuery: "services:shop"})
		Expect(hc.Status.Observations).To(HaveLen(1))
		Expect(hc.Status.Observations[0].Message).To(Equal("#412 Checkout errors, #415 Payment timeouts"))

		hc = reconcileWith(&fakeDatadogAPI{}, "no-incidents", HealthCheckClassDatadogIncidents, map[string]string{AnnotationDatadogIncidentQuery: "services:shop"})
		Expect(hc.Status.Observations[0].Message).To(BeEmpty())
	})

	It("clears observations and keeps lastErrorTime when a previously Alert check can no longer be evaluated", func() {
		api := &fakeDatadogAPI{monitors: map[int64]datadogMonitorState{
			1: {ID: 1, Name: "m", OverallState: "Alert", Groups: map[string]string{"service:a": "Alert"}}}}
		ann := map[string]string{AnnotationDatadogMonitorIDs: "1"}
		failed := reconcileWith(api, "recover-err", HealthCheckClassDatadog, ann)
		Expect(failed.Status.Observations).NotTo(BeEmpty())
		Expect(failed.Status.LastErrorTime).NotTo(BeNil())

		r := &HealthCheckReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), DatadogAPI: erroringAPI{errors.New("HTTP 503")}}
		_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(failed)})
		Expect(err).NotTo(HaveOccurred())
		got := &kuberikrolloutv1alpha1.HealthCheck{}
		Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(failed), got)).To(Succeed())
		Expect(got.Status.Status).To(Equal(kuberikrolloutv1alpha1.HealthStatusPending))
		Expect(*got.Status.Message).To(ContainSubstring("HTTP 503"))
		Expect(got.Status.Observations).To(BeEmpty())
		Expect(got.Status.LastErrorTime.Time).To(Equal(failed.Status.LastErrorTime.Time))
	})

	Describe("cannot-evaluate paths write Pending with the error and no lastErrorTime", func() {
		expectPending := func(hc *kuberikrolloutv1alpha1.HealthCheck, want string) {
			Expect(hc.Status.Status).To(Equal(kuberikrolloutv1alpha1.HealthStatusPending))
			Expect(*hc.Status.Message).To(ContainSubstring(want))
			Expect(hc.Status.LastErrorTime).To(BeNil())
		}

		It("Datadog API error", func() {
			expectPending(reconcileWith(erroringAPI{errors.New("HTTP 503 from Datadog")}, "api-err", HealthCheckClassDatadog,
				map[string]string{AnnotationDatadogMonitorIDs: "1"}), "HTTP 503")
			expectPending(reconcileWith(erroringAPI{errors.New("HTTP 503 from Datadog")}, "inc-err", HealthCheckClassDatadogIncidents,
				map[string]string{AnnotationDatadogIncidentQuery: "x"}), "HTTP 503")
		})

		It("credentials missing", func() {
			for _, v := range []string{datadogAPIKeyEnvVar, datadogAltAPIKeyEnvVar, datadogAppKeyEnvVar, datadogAltAppKeyEnvVar} {
				GinkgoT().Setenv(v, "")
			}
			expectPending(reconcileWith(nil, "no-creds", HealthCheckClassDatadog,
				map[string]string{AnnotationDatadogMonitorIDs: "1"}), "credentials are not configured")
		})

		It("invalid selector", func() {
			api := &fakeDatadogAPI{}
			expectPending(reconcileWith(api, "bad-id", HealthCheckClassDatadog,
				map[string]string{AnnotationDatadogMonitorIDs: "abc"}), "invalid monitor id")
			expectPending(reconcileWith(api, "no-selector", HealthCheckClassDatadog, nil), AnnotationDatadogMonitorIDs)
			expectPending(reconcileWith(api, "no-query", HealthCheckClassDatadogIncidents, nil), AnnotationDatadogIncidentQuery)
		})

		It("monitor not found", func() {
			expectPending(reconcileWith(&fakeDatadogAPI{}, "gone", HealthCheckClassDatadog,
				map[string]string{AnnotationDatadogMonitorIDs: "99"}), "not found")
		})
	})
})
