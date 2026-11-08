// Package netguard keeps agent-driven HTTP fetches off internal networks (SSRF). The dial-time check is the
// guard that matters: it validates the address actually being connected to, so a DNS answer that changes
// between a pre-flight check and the connect (rebinding) cannot reach a private host. CheckURL is only the
// early, readable refusal.
package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

var (
	blockedHosts = map[string]bool{"metadata.google.internal": true, "metadata.goog": true}
	metadataIPs  = map[netip.Addr]bool{
		netip.MustParseAddr("169.254.169.254"): true, // AWS/GCP/Azure/DO/Oracle
		netip.MustParseAddr("169.254.170.2"):   true, // AWS ECS task credentials
		netip.MustParseAddr("169.254.169.253"): true, // Azure wire server
		netip.MustParseAddr("100.100.100.200"): true, // Alibaba
		netip.MustParseAddr("fd00:ec2::254"):   true, // AWS IPv6
	}
	cgnat          = netip.MustParsePrefix("100.64.0.0/10")
	ipv4Translated = netip.MustParsePrefix("::ffff:0:0:0/96") // RFC 2765 ::ffff:0:a.b.c.d
	thisNetwork    = netip.MustParsePrefix("0.0.0.0/8")
	// reserved ranges that Go still calls global unicast: IETF protocol assignments, benchmarking, class E.
	reserved = []netip.Prefix{netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("240.0.0.0/4")}
	// sensitiveParams are unambiguous credential names; words that double as page facets (code, key, sig) are
	// excluded so ordinary browsing is not blocked.
	sensitiveParams = map[string]bool{
		"access_token": true, "api_key": true, "apikey": true, "auth_token": true, "authorization": true,
		"awsaccesskeyid": true, "client_secret": true, "credential": true, "credentials": true, "jwt": true,
		"password": true, "passwd": true, "secret": true, "session_id": true, "signature": true, "token": true,
		"x_amz_security_token": true, "x_amz_signature": true, "x-amz-security-token": true, "x-amz-signature": true,
	}
)

// embedded returns the IPv4 address wrapped in an IPv6 form (::ffff:a.b.c.d or ::ffff:0:a.b.c.d), else ip.
func embedded(ip netip.Addr) netip.Addr {
	ip = ip.Unmap()
	if ip.Is6() && ipv4Translated.Contains(ip) {
		b := ip.As16()
		return netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]})
	}
	return ip
}

func inAny(ps []netip.Prefix, ip netip.Addr) bool {
	for _, p := range ps {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// BlockReason says why ip may not be dialled, or "" when it is a public address.
func BlockReason(ip netip.Addr) string {
	ip = embedded(ip)
	switch {
	case metadataIPs[ip]:
		return "cloud metadata endpoint"
	case ip.IsLoopback():
		return "loopback address"
	case ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast():
		return "link-local address"
	case ip.IsPrivate():
		return "private network address"
	case cgnat.Contains(ip):
		return "carrier-grade NAT address"
	case ip.IsUnspecified() || thisNetwork.Contains(ip):
		return "unspecified address"
	case ip.IsMulticast():
		return "multicast address"
	case inAny(reserved, ip):
		return "reserved address"
	case !ip.IsGlobalUnicast():
		return "non-public address"
	}
	return ""
}

// CheckURL refuses non-http(s) schemes, empty and metadata hostnames, credential-bearing query parameters and
// any host that resolves (now) to a blocked address. DNS failure fails closed.
func CheckURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if s := strings.ToLower(u.Scheme); s != "http" && s != "https" {
		return fmt.Errorf("unsupported URL scheme %q: only http and https are fetched", u.Scheme)
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" {
		return errors.New("URL has no host")
	}
	if blockedHosts[host] {
		return fmt.Errorf("blocked request to internal hostname %s", host)
	}
	if name := SensitiveQueryParam(raw); name != "" {
		return fmt.Errorf("URL carries a credential in query parameter %q; it is not sent to a third party", name)
	}
	var ips []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		ips = []netip.Addr{ip}
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		found, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil || len(found) == 0 {
			return fmt.Errorf("blocked request: DNS resolution failed for %s", host)
		}
		ips = found
	}
	for _, ip := range ips {
		if why := BlockReason(ip.Unmap()); why != "" {
			return fmt.Errorf("blocked request to %s: %s -> %s", why, host, ip)
		}
	}
	return nil
}

// SensitiveQueryParam is the first credential-named query parameter (with a value) in raw, or "".
func SensitiveQueryParam(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.RawQuery == "" {
		return ""
	}
	for _, part := range strings.Split(u.RawQuery, "&") {
		k, v, _ := strings.Cut(part, "=")
		key, kerr := url.QueryUnescape(k)
		val, verr := url.QueryUnescape(v)
		if kerr != nil {
			key = k
		}
		if verr != nil {
			val = v
		}
		if val != "" && sensitiveParams[strings.ToLower(key)] {
			return key
		}
	}
	return ""
}

// control rejects the connection when the address about to be dialled is not public.
func control(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("blocked request: unparseable address %q", host)
	}
	if why := BlockReason(ip); why != "" {
		return fmt.Errorf("blocked request to %s during connect: %s", why, ip)
	}
	return nil
}

// Client is an http.Client whose every connection (including redirect hops) is validated at dial time. It
// ignores proxy settings: a proxy would resolve and connect on the guard's behalf.
func Client(timeout time.Duration) *http.Client {
	d := &net.Dialer{Timeout: 10 * time.Second, Control: control}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:         d.DialContext,
			TLSHandshakeTimeout: 10 * time.Second,
			MaxIdleConns:        4,
			DisableKeepAlives:   true,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("stopped after 5 redirects")
			}
			if s := req.URL.Scheme; s != "http" && s != "https" {
				return fmt.Errorf("redirect to unsupported scheme %q", s)
			}
			if name := SensitiveQueryParam(req.URL.String()); name != "" {
				return fmt.Errorf("redirect carries a credential in query parameter %q", name)
			}
			return nil
		},
	}
}
