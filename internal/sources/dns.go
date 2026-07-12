package sources

import (
	"context"
	"fmt"
	"net"
	"sort"
	"time"

	"github.com/miekg/dns"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	jtov1 "github.com/guided-traffic/jinja-template-operator/api/v1"
)

const (
	// RecordTypeA queries IPv4 addresses.
	RecordTypeA = "A"

	// RecordTypeAAAA queries IPv6 addresses.
	RecordTypeAAAA = "AAAA"

	// RecordTypeDual queries both IPv4 and IPv6 addresses.
	RecordTypeDual = "A+AAAA"

	// maxCNAMEHops limits how many CNAME chain hops are followed before the
	// lookup fails, protecting against CNAME loops.
	maxCNAMEHops = 10

	// defaultDNSPort is used when a configured nameserver has no port.
	defaultDNSPort = "53"
)

// LookupResult is the outcome of a successful DNS lookup.
// NXDOMAIN and empty answers are successful lookups with no IPs.
type LookupResult struct {
	// IPs are the resolved addresses, deduplicated and sorted.
	IPs []string

	// TTL is the minimum TTL over all records involved in the lookup
	// (including intermediate CNAME hops). Zero if no records were returned.
	TTL time.Duration
}

// DNSLookuper resolves a host to IP addresses. Implementations must treat
// NXDOMAIN and empty answers as success with an empty IP list; only transport
// or server errors (timeout, SERVFAIL, ...) are returned as errors.
type DNSLookuper interface {
	Lookup(ctx context.Context, host, recordType, nameserver string) (LookupResult, error)
}

// MiekgLookuper resolves DNS queries via miekg/dns, following CNAME chains
// manually so lookups also work against non-recursive or zone-external targets.
type MiekgLookuper struct {
	// resolvConfPath is the path to resolv.conf used to discover the system
	// default nameserver. Defaults to /etc/resolv.conf.
	resolvConfPath string
}

// NewMiekgLookuper creates a DNSLookuper backed by miekg/dns.
func NewMiekgLookuper() *MiekgLookuper {
	return &MiekgLookuper{resolvConfPath: "/etc/resolv.conf"}
}

// Lookup resolves host to IP addresses of the given record type
// (A, AAAA or A+AAAA), following CNAME chains up to maxCNAMEHops.
func (l *MiekgLookuper) Lookup(ctx context.Context, host, recordType, nameserver string) (LookupResult, error) {
	server, err := l.serverAddr(nameserver)
	if err != nil {
		return LookupResult{}, err
	}

	var qtypes []uint16
	switch recordType {
	case "", RecordTypeA:
		qtypes = []uint16{dns.TypeA}
	case RecordTypeAAAA:
		qtypes = []uint16{dns.TypeAAAA}
	case RecordTypeDual:
		qtypes = []uint16{dns.TypeA, dns.TypeAAAA}
	default:
		return LookupResult{}, fmt.Errorf("unsupported record type %q", recordType)
	}

	seen := make(map[string]struct{})
	result := LookupResult{}
	for _, qtype := range qtypes {
		ips, ttl, err := l.queryChain(ctx, server, host, qtype)
		if err != nil {
			return LookupResult{}, err
		}
		for _, ip := range ips {
			if _, dup := seen[ip]; !dup {
				seen[ip] = struct{}{}
				result.IPs = append(result.IPs, ip)
			}
		}
		if ttl > 0 && (result.TTL == 0 || ttl < result.TTL) {
			result.TTL = ttl
		}
	}

	sort.Strings(result.IPs)
	return result, nil
}

// queryChain resolves host for a single query type, following CNAME answers
// until IP records are found, the chain ends empty, or the hop limit is hit.
func (l *MiekgLookuper) queryChain(ctx context.Context, server, host string, qtype uint16) ([]string, time.Duration, error) {
	current := dns.Fqdn(host)
	var minTTL time.Duration

	for hop := 0; hop <= maxCNAMEHops; hop++ {
		resp, err := l.exchange(ctx, server, current, qtype)
		if err != nil {
			return nil, 0, fmt.Errorf("query %s %s @%s: %w", dns.TypeToString[qtype], current, server, err)
		}

		switch resp.Rcode {
		case dns.RcodeSuccess:
			// Proceed with the answer section.
		case dns.RcodeNameError:
			// NXDOMAIN: authoritative "does not exist" — empty result, not an error.
			return nil, minTTL, nil
		default:
			return nil, 0, fmt.Errorf("query %s %s @%s: server returned %s",
				dns.TypeToString[qtype], current, server, dns.RcodeToString[resp.Rcode])
		}

		var ips []string
		cnames := make(map[string]string)
		for _, rr := range resp.Answer {
			ttl := time.Duration(rr.Header().Ttl) * time.Second
			if ttl > 0 && (minTTL == 0 || ttl < minTTL) {
				minTTL = ttl
			}
			switch record := rr.(type) {
			case *dns.A:
				if qtype == dns.TypeA {
					ips = append(ips, record.A.String())
				}
			case *dns.AAAA:
				if qtype == dns.TypeAAAA {
					ips = append(ips, record.AAAA.String())
				}
			case *dns.CNAME:
				cnames[record.Header().Name] = record.Target
			}
		}

		if len(ips) > 0 {
			return ips, minTTL, nil
		}

		target, ok := cnames[current]
		if !ok {
			// NODATA: name exists but has no records of this type.
			return nil, minTTL, nil
		}
		current = target
	}

	return nil, 0, fmt.Errorf("query %s %s: CNAME chain exceeds %d hops", dns.TypeToString[qtype], host, maxCNAMEHops)
}

// exchange sends a single DNS query, retrying over TCP on truncation.
func (l *MiekgLookuper) exchange(ctx context.Context, server, name string, qtype uint16) (*dns.Msg, error) {
	msg := new(dns.Msg)
	msg.SetQuestion(name, qtype)
	msg.RecursionDesired = true

	client := &dns.Client{}
	resp, _, err := client.ExchangeContext(ctx, msg, server)
	if err != nil {
		return nil, err
	}
	if resp.Truncated {
		client.Net = "tcp"
		resp, _, err = client.ExchangeContext(ctx, msg, server)
		if err != nil {
			return nil, err
		}
	}
	return resp, nil
}

// serverAddr returns the nameserver address to use as "host:port". An empty
// nameserver falls back to the first system resolver from resolv.conf.
func (l *MiekgLookuper) serverAddr(nameserver string) (string, error) {
	if nameserver == "" {
		conf, err := dns.ClientConfigFromFile(l.resolvConfPath)
		if err != nil {
			return "", fmt.Errorf("failed to read system resolver config: %w", err)
		}
		if len(conf.Servers) == 0 {
			return "", fmt.Errorf("no nameservers found in %s", l.resolvConfPath)
		}
		return net.JoinHostPort(conf.Servers[0], conf.Port), nil
	}

	if _, _, err := net.SplitHostPort(nameserver); err == nil {
		return nameserver, nil
	}
	return net.JoinHostPort(nameserver, defaultDNSPort), nil
}

// MergeDNSRecords merges a fresh (successful) lookup result into the
// previously known records, applying the removal grace period:
//   - values in current get LastSeen = now (new values are added immediately),
//   - values missing from current stay until LastSeen + grace has passed.
//
// It returns the effective records sorted by value and the duration until the
// next grace-period expiry (0 if no record is pending removal).
func MergeDNSRecords(prev []jtov1.DNSRecord, current []string, now metav1.Time, grace time.Duration) ([]jtov1.DNSRecord, time.Duration) {
	currentSet := make(map[string]struct{}, len(current))
	for _, v := range current {
		currentSet[v] = struct{}{}
	}

	merged := make([]jtov1.DNSRecord, 0, len(current))
	var nextExpiry time.Duration

	for _, rec := range prev {
		if _, stillPresent := currentSet[rec.Value]; stillPresent {
			continue // Re-added below with a fresh LastSeen.
		}
		expiresIn := rec.LastSeen.Add(grace).Sub(now.Time)
		if expiresIn <= 0 {
			continue // Grace period over — drop the record.
		}
		merged = append(merged, rec)
		if nextExpiry == 0 || expiresIn < nextExpiry {
			nextExpiry = expiresIn
		}
	}

	for _, v := range current {
		merged = append(merged, jtov1.DNSRecord{Value: v, LastSeen: now})
	}

	sort.Slice(merged, func(i, j int) bool { return merged[i].Value < merged[j].Value })
	return merged, nextExpiry
}

// RecordValues extracts the sorted list of values from DNS records for use as
// a template context variable.
func RecordValues(records []jtov1.DNSRecord) []string {
	values := make([]string, 0, len(records))
	for _, rec := range records {
		values = append(values, rec.Value)
	}
	sort.Strings(values)
	return values
}
