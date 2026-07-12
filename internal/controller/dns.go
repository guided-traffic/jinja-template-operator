package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	jtov1 "github.com/guided-traffic/jinja-template-operator/api/v1"
	"github.com/guided-traffic/jinja-template-operator/internal/sources"
)

const (
	// ConditionDNSHealthy reports whether all DNS source lookups succeed.
	// It turns False on lookup failures while Ready stays True and the last
	// known records remain in use.
	ConditionDNSHealthy = "DNSHealthy"

	// ReasonDNSLookupFailed indicates one or more DNS lookups failed.
	ReasonDNSLookupFailed = "DNSLookupFailed"

	// ReasonDNSLookupsSucceeded indicates all DNS lookups succeeded.
	ReasonDNSLookupsSucceeded = "DNSLookupsSucceeded"

	// defaultDNSRetryInterval is the requeue interval after a failed lookup
	// (or an empty result without TTL) when no refresh interval is configured.
	defaultDNSRetryInterval = 30 * time.Second

	// minDNSRefreshInterval floors TTL-driven refreshes to avoid hot-looping
	// on records with very small TTLs.
	minDNSRefreshInterval = 5 * time.Second

	// minDNSRequeue floors the overall DNS requeue duration.
	minDNSRequeue = time.Second
)

// defaultDNSLookuper is used when no lookuper is injected (production wiring).
var defaultDNSLookuper sources.DNSLookuper = sources.NewMiekgLookuper()

// lookuper returns the injected DNSLookuper or the miekg/dns default.
func (r *JinjaTemplateReconciler) lookuper() sources.DNSLookuper {
	if r.DNSLookuper != nil {
		return r.DNSLookuper
	}
	return defaultDNSLookuper
}

// resolveDNSSources performs the lookups for all DNS sources, merges results
// with the previously known state in status (grace periods, stale-on-error)
// and returns the template values per source name plus the duration after
// which the next DNS-driven reconcile is due (0 if there are no DNS sources).
//
// A lookup failure for a source with previously known records keeps the last
// known state and flips the DNSHealthy condition to False. A failure without
// any previous state is returned as an error (Ready=False path).
func (r *JinjaTemplateReconciler) resolveDNSSources(
	ctx context.Context,
	log logr.Logger,
	jt *jtov1.JinjaTemplate,
) (map[string][]string, time.Duration, error) {
	now := metav1.Now()
	values := make(map[string][]string)
	var newStatuses []jtov1.DNSSourceStatus
	var requeue time.Duration
	var errMsgs []string

	for _, src := range jt.Spec.Sources {
		if src.DNS == nil {
			continue
		}

		prev := findDNSSourceStatus(jt.Status.DNSSources, src.Name)
		res, err := r.lookuper().Lookup(ctx, src.DNS.Host, src.DNS.RecordType, src.DNS.Nameserver)
		if err != nil {
			if prev == nil || prev.LastSuccessfulLookup == nil {
				return nil, 0, fmt.Errorf("dns source %q: initial lookup failed: %w", src.Name, err)
			}

			// Stale-on-error: keep the last known records, do not age them.
			log.Info("DNS lookup failed, keeping last known records",
				"source", src.Name, "host", src.DNS.Host, "error", err.Error())
			st := *prev.DeepCopy()
			st.LastError = err.Error()
			newStatuses = append(newStatuses, st)
			values[src.Name] = sources.RecordValues(st.Records)
			errMsgs = append(errMsgs, fmt.Sprintf("source %q: %v", src.Name, err))
			requeue = minNonZero(requeue, dnsRetryInterval(src.DNS))
			continue
		}

		grace := time.Duration(0)
		if src.DNS.RemovalGracePeriodSeconds != nil {
			grace = time.Duration(*src.DNS.RemovalGracePeriodSeconds) * time.Second
		}

		var prevRecords []jtov1.DNSRecord
		if prev != nil {
			prevRecords = prev.Records
		}
		merged, nextExpiry := sources.MergeDNSRecords(prevRecords, res.IPs, now, grace)

		newStatuses = append(newStatuses, jtov1.DNSSourceStatus{
			Name:                 src.Name,
			Records:              merged,
			LastSuccessfulLookup: &now,
		})
		values[src.Name] = sources.RecordValues(merged)

		requeue = minNonZero(requeue, dnsRefreshInterval(src.DNS, res.TTL))
		if nextExpiry > 0 {
			requeue = minNonZero(requeue, nextExpiry)
		}
	}

	// Replace the tracked DNS state wholesale: removed sources are pruned.
	jt.Status.DNSSources = newStatuses

	if len(newStatuses) == 0 {
		removeCondition(jt, ConditionDNSHealthy)
		return values, 0, nil
	}

	if len(errMsgs) > 0 {
		msg := strings.Join(errMsgs, "; ")
		r.setConditionOfType(jt, ConditionDNSHealthy, metav1.ConditionFalse, ReasonDNSLookupFailed, msg)
		r.Recorder.Eventf(jt, nil, corev1.EventTypeWarning, ReasonDNSLookupFailed, "Reconcile",
			"DNS lookup failed, using last known records: %s", msg)
	} else {
		r.setConditionOfType(jt, ConditionDNSHealthy, metav1.ConditionTrue, ReasonDNSLookupsSucceeded,
			"All DNS lookups succeeded")
	}

	if requeue > 0 && requeue < minDNSRequeue {
		requeue = minDNSRequeue
	}
	return values, requeue, nil
}

// dnsRefreshInterval returns the requeue duration after a successful lookup:
// the configured refresh interval if set, otherwise the response TTL (floored),
// falling back to the default retry interval for empty responses without TTL.
func dnsRefreshInterval(src *jtov1.DNSSource, ttl time.Duration) time.Duration {
	if src.RefreshIntervalSeconds != nil {
		return time.Duration(*src.RefreshIntervalSeconds) * time.Second
	}
	if ttl > 0 {
		if ttl < minDNSRefreshInterval {
			return minDNSRefreshInterval
		}
		return ttl
	}
	return defaultDNSRetryInterval
}

// dnsRetryInterval returns the requeue duration after a failed lookup.
func dnsRetryInterval(src *jtov1.DNSSource) time.Duration {
	if src.RefreshIntervalSeconds != nil {
		return time.Duration(*src.RefreshIntervalSeconds) * time.Second
	}
	return defaultDNSRetryInterval
}

// findDNSSourceStatus returns the tracked status for a DNS source by name.
func findDNSSourceStatus(statuses []jtov1.DNSSourceStatus, name string) *jtov1.DNSSourceStatus {
	for i := range statuses {
		if statuses[i].Name == name {
			return &statuses[i]
		}
	}
	return nil
}

// minNonZero returns the smaller of two durations, treating zero as "unset".
func minNonZero(a, b time.Duration) time.Duration {
	if a == 0 {
		return b
	}
	if b == 0 {
		return a
	}
	if a < b {
		return a
	}
	return b
}
