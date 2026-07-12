package sources

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	jtov1 "github.com/guided-traffic/jinja-template-operator/api/v1"
)

// startTestDNSServer runs a local UDP DNS server for the duration of the test
// and returns its address.
func startTestDNSServer(t *testing.T, handler dns.HandlerFunc) string {
	t.Helper()

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)

	server := &dns.Server{PacketConn: pc, Handler: handler}
	go func() { _ = server.ActivateAndServe() }()
	t.Cleanup(func() { _ = server.Shutdown() })

	return pc.LocalAddr().String()
}

// rr parses a record in zone file syntax, failing the test on error.
func rr(t *testing.T, s string) dns.RR {
	t.Helper()
	record, err := dns.NewRR(s)
	require.NoError(t, err)
	return record
}

// staticHandler answers queries from a map of qname+qtype to answer records.
func staticHandler(answers map[string][]dns.RR, rcodes map[string]int) dns.HandlerFunc {
	return func(w dns.ResponseWriter, req *dns.Msg) {
		resp := new(dns.Msg)
		resp.SetReply(req)
		q := req.Question[0]
		key := q.Name + dns.TypeToString[q.Qtype]
		if rcode, ok := rcodes[q.Name]; ok {
			resp.Rcode = rcode
		} else {
			resp.Answer = answers[key]
		}
		_ = w.WriteMsg(resp)
	}
}

func TestLookupA(t *testing.T) {
	addr := startTestDNSServer(t, staticHandler(map[string][]dns.RR{
		"app.example.com.A": {
			rr(t, "app.example.com. 120 IN A 10.0.0.2"),
			rr(t, "app.example.com. 60 IN A 10.0.0.1"),
		},
	}, nil))

	res, err := NewMiekgLookuper().Lookup(context.Background(), "app.example.com", RecordTypeA, addr)
	require.NoError(t, err)
	assert.Equal(t, []string{"10.0.0.1", "10.0.0.2"}, res.IPs)
	assert.Equal(t, 60*time.Second, res.TTL)
}

func TestLookupDualStack(t *testing.T) {
	addr := startTestDNSServer(t, staticHandler(map[string][]dns.RR{
		"app.example.com.A":    {rr(t, "app.example.com. 60 IN A 10.0.0.1")},
		"app.example.com.AAAA": {rr(t, "app.example.com. 30 IN AAAA 2001:db8::1")},
	}, nil))

	res, err := NewMiekgLookuper().Lookup(context.Background(), "app.example.com", RecordTypeDual, addr)
	require.NoError(t, err)
	assert.Equal(t, []string{"10.0.0.1", "2001:db8::1"}, res.IPs)
	assert.Equal(t, 30*time.Second, res.TTL)
}

func TestLookupFollowsCNAMEChain(t *testing.T) {
	addr := startTestDNSServer(t, staticHandler(map[string][]dns.RR{
		"www.example.com.A":  {rr(t, "www.example.com. 300 IN CNAME lb.example.com.")},
		"lb.example.com.A":   {rr(t, "lb.example.com. 300 IN CNAME node.example.com.")},
		"node.example.com.A": {rr(t, "node.example.com. 60 IN A 10.0.0.9")},
	}, nil))

	res, err := NewMiekgLookuper().Lookup(context.Background(), "www.example.com", RecordTypeA, addr)
	require.NoError(t, err)
	assert.Equal(t, []string{"10.0.0.9"}, res.IPs)
	assert.Equal(t, 60*time.Second, res.TTL)
}

func TestLookupCNAMELoopFails(t *testing.T) {
	addr := startTestDNSServer(t, staticHandler(map[string][]dns.RR{
		"a.example.com.A": {rr(t, "a.example.com. 300 IN CNAME b.example.com.")},
		"b.example.com.A": {rr(t, "b.example.com. 300 IN CNAME a.example.com.")},
	}, nil))

	_, err := NewMiekgLookuper().Lookup(context.Background(), "a.example.com", RecordTypeA, addr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CNAME chain exceeds")
}

func TestLookupNXDomainIsEmptySuccess(t *testing.T) {
	addr := startTestDNSServer(t, staticHandler(nil, map[string]int{
		"gone.example.com.": dns.RcodeNameError,
	}))

	res, err := NewMiekgLookuper().Lookup(context.Background(), "gone.example.com", RecordTypeA, addr)
	require.NoError(t, err)
	assert.Empty(t, res.IPs)
}

func TestLookupNoDataIsEmptySuccess(t *testing.T) {
	addr := startTestDNSServer(t, staticHandler(map[string][]dns.RR{}, nil))

	res, err := NewMiekgLookuper().Lookup(context.Background(), "app.example.com", RecordTypeAAAA, addr)
	require.NoError(t, err)
	assert.Empty(t, res.IPs)
}

func TestLookupServFailIsError(t *testing.T) {
	addr := startTestDNSServer(t, staticHandler(nil, map[string]int{
		"broken.example.com.": dns.RcodeServerFailure,
	}))

	_, err := NewMiekgLookuper().Lookup(context.Background(), "broken.example.com", RecordTypeA, addr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SERVFAIL")
}

func TestLookupUnsupportedRecordType(t *testing.T) {
	_, err := NewMiekgLookuper().Lookup(context.Background(), "app.example.com", "MX", "127.0.0.1:53")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported record type")
}

func TestServerAddrDefaultsPort(t *testing.T) {
	l := NewMiekgLookuper()

	addr, err := l.serverAddr("10.96.0.10")
	require.NoError(t, err)
	assert.Equal(t, "10.96.0.10:53", addr)

	addr, err = l.serverAddr("10.96.0.10:5353")
	require.NoError(t, err)
	assert.Equal(t, "10.96.0.10:5353", addr)
}

func TestMergeDNSRecordsAddsAndRefreshes(t *testing.T) {
	now := metav1.Now()
	earlier := metav1.NewTime(now.Add(-time.Minute))
	prev := []jtov1.DNSRecord{{Value: "10.0.0.1", LastSeen: earlier}}

	merged, nextExpiry := MergeDNSRecords(prev, []string{"10.0.0.2", "10.0.0.1"}, now, 5*time.Minute)

	require.Len(t, merged, 2)
	assert.Equal(t, "10.0.0.1", merged[0].Value)
	assert.Equal(t, now, merged[0].LastSeen, "still-present record must get a fresh LastSeen")
	assert.Equal(t, "10.0.0.2", merged[1].Value)
	assert.Zero(t, nextExpiry)
}

func TestMergeDNSRecordsGraceKeepsDisappeared(t *testing.T) {
	now := metav1.Now()
	twoMinAgo := metav1.NewTime(now.Add(-2 * time.Minute))
	prev := []jtov1.DNSRecord{{Value: "10.0.0.1", LastSeen: twoMinAgo}}

	merged, nextExpiry := MergeDNSRecords(prev, []string{"10.0.0.2"}, now, 5*time.Minute)

	require.Len(t, merged, 2)
	assert.Equal(t, "10.0.0.1", merged[0].Value)
	assert.Equal(t, twoMinAgo, merged[0].LastSeen, "disappeared record keeps its old LastSeen")
	assert.Equal(t, 3*time.Minute, nextExpiry)
}

func TestMergeDNSRecordsGraceExpiredDrops(t *testing.T) {
	now := metav1.Now()
	tenMinAgo := metav1.NewTime(now.Add(-10 * time.Minute))
	prev := []jtov1.DNSRecord{{Value: "10.0.0.1", LastSeen: tenMinAgo}}

	merged, nextExpiry := MergeDNSRecords(prev, []string{"10.0.0.2"}, now, 5*time.Minute)

	require.Len(t, merged, 1)
	assert.Equal(t, "10.0.0.2", merged[0].Value)
	assert.Zero(t, nextExpiry)
}

func TestMergeDNSRecordsNoGraceDropsImmediately(t *testing.T) {
	now := metav1.Now()
	prev := []jtov1.DNSRecord{{Value: "10.0.0.1", LastSeen: now}}

	merged, nextExpiry := MergeDNSRecords(prev, nil, now, 0)

	assert.Empty(t, merged)
	assert.Zero(t, nextExpiry)
}

func TestRecordValues(t *testing.T) {
	now := metav1.Now()
	records := []jtov1.DNSRecord{
		{Value: "10.0.0.2", LastSeen: now},
		{Value: "10.0.0.1", LastSeen: now},
	}
	assert.Equal(t, []string{"10.0.0.1", "10.0.0.2"}, RecordValues(records))
	assert.Empty(t, RecordValues(nil))
}
