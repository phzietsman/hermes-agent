package netguard

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestBlockReason(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "::1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "fd12::1", "169.254.169.254", "169.254.1.1",
		"169.254.170.2", "100.64.0.1", "100.100.100.200", "0.0.0.0", "0.1.2.3", "::", "224.0.0.1", "ff02::1",
		"::ffff:127.0.0.1", "::ffff:10.0.0.1", "::ffff:169.254.169.254", "::ffff:0:127.0.0.1", "fd00:ec2::254", "fe80::1",
		"240.0.0.1", "255.255.255.255",
	}
	for _, s := range blocked {
		if BlockReason(netip.MustParseAddr(s)) == "" {
			t.Errorf("%s must be blocked", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "1.1.1.1", "93.184.216.34", "2606:4700:4700::1111", "::ffff:8.8.8.8"} {
		if why := BlockReason(netip.MustParseAddr(s)); why != "" {
			t.Errorf("%s blocked (%s)", s, why)
		}
	}
}

func TestCheckURL(t *testing.T) {
	bad := map[string]string{
		"file:///etc/passwd":                       "scheme",
		"ftp://example.com/":                       "scheme",
		"http://":                                  "no host",
		"http://metadata.google.internal/x":        "internal hostname",
		"http://metadata.goog./x":                  "internal hostname",
		"http://127.0.0.1:8080/":                   "loopback",
		"http://[::1]/":                            "loopback",
		"http://169.254.169.254/latest/meta-data/": "metadata",
		"http://[::ffff:10.0.0.1]/":                "private",
		"https://example.com/?api_key=abc":         "credential",
		"https://example.com/?x=1&TOKEN=abc":       "credential",
		"https://example.com/?a%5Fb=1&%74oken=z":   "credential",
	}
	for u, want := range bad {
		err := CheckURL(u)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("CheckURL(%q) = %v, want error containing %q", u, err, want)
		}
	}
	// A bare-word facet and an empty credential value are ordinary browsing.
	for _, u := range []string{"http://8.8.8.8/?code=1&key=2&auth=3&session=4&sig=5", "http://8.8.8.8/?token="} {
		if err := CheckURL(u); err != nil {
			t.Errorf("CheckURL(%q) = %v", u, err)
		}
	}
}

func TestClientRefusesLoopbackAtDialTime(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("secret")) }))
	defer srv.Close()
	_, err := Client(5 * time.Second).Get(srv.URL)
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("a loopback server was reachable through the guarded client: %v", err)
	}
}

func TestClientRefusesRedirectToCredentialURL(t *testing.T) {
	c := Client(time.Second)
	req, _ := http.NewRequest("GET", "http://8.8.8.8/?token=abc", nil)
	if err := c.CheckRedirect(req, nil); err == nil {
		t.Error("redirect carrying a credential must be refused")
	}
	if err := c.CheckRedirect(req, make([]*http.Request, 5)); err == nil {
		t.Error("redirect chain must be bounded")
	}
	ftp, _ := http.NewRequest("GET", "ftp://example.com/", nil)
	if err := c.CheckRedirect(ftp, nil); err == nil {
		t.Error("redirect to a non-http scheme must be refused")
	}
}

func FuzzSensitiveQueryParam(f *testing.F) {
	for _, s := range []string{"http://a/?token=1", "http://a/?%zz=1", "", "http://a/?&&=", "http://a/?token"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) { _ = SensitiveQueryParam(s) })
}
