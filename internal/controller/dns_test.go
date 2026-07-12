package controller

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	jtov1 "github.com/guided-traffic/jinja-template-operator/api/v1"
	"github.com/guided-traffic/jinja-template-operator/internal/sources"
)

// fakeLookuper serves canned lookup results per host.
type fakeLookuper struct {
	results map[string]sources.LookupResult
	errs    map[string]error
	calls   int
}

func (f *fakeLookuper) Lookup(_ context.Context, host, _, _ string) (sources.LookupResult, error) {
	f.calls++
	if err, ok := f.errs[host]; ok {
		return sources.LookupResult{}, err
	}
	return f.results[host], nil
}

func int32Ptr(v int32) *int32 { return &v }

func dnsJinjaTemplate(dnsSrc *jtov1.DNSSource) *jtov1.JinjaTemplate {
	return &jtov1.JinjaTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "dns-config",
			Namespace: "default",
		},
		Spec: jtov1.JinjaTemplateSpec{
			Sources:  []jtov1.Source{{Name: "ips", DNS: dnsSrc}},
			Template: "{% for ip in ips %}{{ ip }};{% endfor %}",
			Output:   jtov1.Output{Kind: "ConfigMap"},
		},
	}
}

func findCondition(jt *jtov1.JinjaTemplate, condType string) *metav1.Condition {
	for i := range jt.Status.Conditions {
		if jt.Status.Conditions[i].Type == condType {
			return &jt.Status.Conditions[i]
		}
	}
	return nil
}

func TestReconcileDNSSourceRendersIPs(t *testing.T) {
	jt := dnsJinjaTemplate(&jtov1.DNSSource{Host: "app.example.com"})
	reconciler, _ := newTestReconciler(jt)
	reconciler.DNSLookuper = &fakeLookuper{results: map[string]sources.LookupResult{
		"app.example.com": {IPs: []string{"10.0.0.1", "10.0.0.2"}, TTL: 120 * time.Second},
	}}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "dns-config", Namespace: "default"},
	})
	require.NoError(t, err)
	assert.Equal(t, 120*time.Second, result.RequeueAfter, "TTL drives the requeue")

	cm := &corev1.ConfigMap{}
	require.NoError(t, reconciler.Get(context.Background(),
		types.NamespacedName{Name: "dns-config", Namespace: "default"}, cm))
	assert.Equal(t, "10.0.0.1;10.0.0.2;", cm.Data["content"])

	updated := &jtov1.JinjaTemplate{}
	require.NoError(t, reconciler.Get(context.Background(),
		types.NamespacedName{Name: "dns-config", Namespace: "default"}, updated))

	require.Len(t, updated.Status.DNSSources, 1)
	assert.Equal(t, "ips", updated.Status.DNSSources[0].Name)
	assert.Len(t, updated.Status.DNSSources[0].Records, 2)
	assert.NotNil(t, updated.Status.DNSSources[0].LastSuccessfulLookup)
	assert.Empty(t, updated.Status.DNSSources[0].LastError)

	healthy := findCondition(updated, ConditionDNSHealthy)
	require.NotNil(t, healthy)
	assert.Equal(t, metav1.ConditionTrue, healthy.Status)
	ready := findCondition(updated, ConditionReady)
	require.NotNil(t, ready)
	assert.Equal(t, metav1.ConditionTrue, ready.Status)
}

func TestReconcileDNSRefreshIntervalOverridesTTL(t *testing.T) {
	jt := dnsJinjaTemplate(&jtov1.DNSSource{
		Host:                   "app.example.com",
		RefreshIntervalSeconds: int32Ptr(15),
	})
	reconciler, _ := newTestReconciler(jt)
	reconciler.DNSLookuper = &fakeLookuper{results: map[string]sources.LookupResult{
		"app.example.com": {IPs: []string{"10.0.0.1"}, TTL: 3600 * time.Second},
	}}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "dns-config", Namespace: "default"},
	})
	require.NoError(t, err)
	assert.Equal(t, 15*time.Second, result.RequeueAfter)
}

func TestReconcileDNSFirstLookupFailureSetsReadyFalse(t *testing.T) {
	jt := dnsJinjaTemplate(&jtov1.DNSSource{Host: "app.example.com"})
	reconciler, _ := newTestReconciler(jt)
	reconciler.DNSLookuper = &fakeLookuper{errs: map[string]error{
		"app.example.com": fmt.Errorf("connection timed out"),
	}}

	_, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "dns-config", Namespace: "default"},
	})
	require.Error(t, err, "initial lookup failure must surface as reconcile error")

	updated := &jtov1.JinjaTemplate{}
	require.NoError(t, reconciler.Get(context.Background(),
		types.NamespacedName{Name: "dns-config", Namespace: "default"}, updated))

	ready := findCondition(updated, ConditionReady)
	require.NotNil(t, ready)
	assert.Equal(t, metav1.ConditionFalse, ready.Status)
	assert.Equal(t, ReasonDNSLookupFailed, ready.Reason)
}

func TestReconcileDNSFailureKeepsLastKnownRecords(t *testing.T) {
	jt := dnsJinjaTemplate(&jtov1.DNSSource{Host: "app.example.com"})
	lookuper := &fakeLookuper{results: map[string]sources.LookupResult{
		"app.example.com": {IPs: []string{"10.0.0.1"}, TTL: 60 * time.Second},
	}}
	reconciler, _ := newTestReconciler(jt)
	reconciler.DNSLookuper = lookuper

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "dns-config", Namespace: "default"}}

	// First reconcile succeeds and records state.
	_, err := reconciler.Reconcile(context.Background(), req)
	require.NoError(t, err)

	// Second reconcile: lookup fails, last known records stay in the output.
	lookuper.errs = map[string]error{"app.example.com": fmt.Errorf("SERVFAIL")}
	result, err := reconciler.Reconcile(context.Background(), req)
	require.NoError(t, err, "stale-on-error must not surface as reconcile error")
	assert.Equal(t, defaultDNSRetryInterval, result.RequeueAfter)

	cm := &corev1.ConfigMap{}
	require.NoError(t, reconciler.Get(context.Background(),
		types.NamespacedName{Name: "dns-config", Namespace: "default"}, cm))
	assert.Equal(t, "10.0.0.1;", cm.Data["content"], "output keeps last known IPs")

	updated := &jtov1.JinjaTemplate{}
	require.NoError(t, reconciler.Get(context.Background(), req.NamespacedName, updated))

	require.Len(t, updated.Status.DNSSources, 1)
	assert.Contains(t, updated.Status.DNSSources[0].LastError, "SERVFAIL")
	assert.NotNil(t, updated.Status.DNSSources[0].LastSuccessfulLookup)

	healthy := findCondition(updated, ConditionDNSHealthy)
	require.NotNil(t, healthy)
	assert.Equal(t, metav1.ConditionFalse, healthy.Status)
	ready := findCondition(updated, ConditionReady)
	require.NotNil(t, ready)
	assert.Equal(t, metav1.ConditionTrue, ready.Status, "Ready stays True on stale records")
}

func TestReconcileDNSGracePeriodHoldsRemovedRecord(t *testing.T) {
	jt := dnsJinjaTemplate(&jtov1.DNSSource{
		Host:                      "app.example.com",
		RemovalGracePeriodSeconds: int32Ptr(300),
	})
	lookuper := &fakeLookuper{results: map[string]sources.LookupResult{
		"app.example.com": {IPs: []string{"10.0.0.1", "10.0.0.2"}, TTL: 600 * time.Second},
	}}
	reconciler, _ := newTestReconciler(jt)
	reconciler.DNSLookuper = lookuper

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "dns-config", Namespace: "default"}}
	_, err := reconciler.Reconcile(context.Background(), req)
	require.NoError(t, err)

	// One IP disappears from DNS; grace period keeps it in the list.
	lookuper.results["app.example.com"] = sources.LookupResult{IPs: []string{"10.0.0.2"}, TTL: 600 * time.Second}
	result, err := reconciler.Reconcile(context.Background(), req)
	require.NoError(t, err)

	cm := &corev1.ConfigMap{}
	require.NoError(t, reconciler.Get(context.Background(),
		types.NamespacedName{Name: "dns-config", Namespace: "default"}, cm))
	assert.Equal(t, "10.0.0.1;10.0.0.2;", cm.Data["content"], "removed IP held through grace period")

	assert.LessOrEqual(t, result.RequeueAfter, 300*time.Second,
		"requeue must not be later than the grace expiry")
	assert.Positive(t, result.RequeueAfter)
}

func TestReconcileDNSSourceRemovalPrunesStatus(t *testing.T) {
	jt := dnsJinjaTemplate(&jtov1.DNSSource{Host: "app.example.com"})
	lookuper := &fakeLookuper{results: map[string]sources.LookupResult{
		"app.example.com": {IPs: []string{"10.0.0.1"}, TTL: 60 * time.Second},
	}}
	reconciler, _ := newTestReconciler(jt)
	reconciler.DNSLookuper = lookuper

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "dns-config", Namespace: "default"}}
	_, err := reconciler.Reconcile(context.Background(), req)
	require.NoError(t, err)

	// Replace the DNS source with an inline-only spec.
	updated := &jtov1.JinjaTemplate{}
	require.NoError(t, reconciler.Get(context.Background(), req.NamespacedName, updated))
	updated.Spec.Sources = nil
	updated.Spec.Template = "static"
	require.NoError(t, reconciler.Update(context.Background(), updated))

	result, err := reconciler.Reconcile(context.Background(), req)
	require.NoError(t, err)
	assert.Zero(t, result.RequeueAfter)

	require.NoError(t, reconciler.Get(context.Background(), req.NamespacedName, updated))
	assert.Empty(t, updated.Status.DNSSources)
	assert.Nil(t, findCondition(updated, ConditionDNSHealthy), "DNSHealthy condition removed with last DNS source")
}

func TestValidateSourcesDNS(t *testing.T) {
	require.NoError(t, validateSources([]jtov1.Source{
		{Name: "ips", DNS: &jtov1.DNSSource{Host: "app.example.com"}},
	}))

	err := validateSources([]jtov1.Source{
		{Name: "ips", DNS: &jtov1.DNSSource{Host: "app.example.com"}, ConfigMap: &jtov1.ConfigMapSource{Name: "cm", Key: "k"}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one of")

	err = validateSources([]jtov1.Source{{Name: "ips", DNS: &jtov1.DNSSource{}}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dns.host")
}

func TestDNSRefreshIntervalFloorsAndFallsBack(t *testing.T) {
	src := &jtov1.DNSSource{}
	assert.Equal(t, minDNSRefreshInterval, dnsRefreshInterval(src, time.Second), "tiny TTLs are floored")
	assert.Equal(t, defaultDNSRetryInterval, dnsRefreshInterval(src, 0), "no TTL falls back to default")
	assert.Equal(t, 42*time.Second, dnsRefreshInterval(src, 42*time.Second))

	src.RefreshIntervalSeconds = int32Ptr(7)
	assert.Equal(t, 7*time.Second, dnsRefreshInterval(src, 42*time.Second), "interval wins over TTL")
}

func TestLookuperDefaultsToMiekg(t *testing.T) {
	reconciler, _ := newTestReconciler()
	assert.Same(t, defaultDNSLookuper, reconciler.lookuper())

	injected := &fakeLookuper{}
	reconciler.DNSLookuper = injected
	assert.Same(t, sources.DNSLookuper(injected), reconciler.lookuper())
}

func TestDNSRetryInterval(t *testing.T) {
	assert.Equal(t, defaultDNSRetryInterval, dnsRetryInterval(&jtov1.DNSSource{}))
	assert.Equal(t, 12*time.Second, dnsRetryInterval(&jtov1.DNSSource{RefreshIntervalSeconds: int32Ptr(12)}))
}

func TestMinNonZero(t *testing.T) {
	assert.Equal(t, time.Duration(0), minNonZero(0, 0))
	assert.Equal(t, 5*time.Second, minNonZero(0, 5*time.Second))
	assert.Equal(t, 5*time.Second, minNonZero(5*time.Second, 0))
	assert.Equal(t, 3*time.Second, minNonZero(3*time.Second, 5*time.Second))
	assert.Equal(t, 3*time.Second, minNonZero(5*time.Second, 3*time.Second))
}
