package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const cfDomainsURL = "https://raw.githubusercontent.com/Flowseal/tg-ws-proxy/main/.github/cfproxy-domains.txt"

var cfEncodedDefaults = []string{
	"virkgj.com", "vmmzovy.com", "mkuosckvso.com", "zaewayzmplad.com",
	"twdmbzcm.com", "awzwsldi.com", "clngqrflngqin.com", "tjacxbqtj.com",
	"bxaxtxmrw.com", "dmohrsgmohcrwb.com", "vwbmtmoi.com", "khgrre.com",
	"ulihssf.com", "tmhqsdqmfpmk.com", "xwuwoqbm.com", "orgcnunpj.com",
	"zhkuldz.com", "zypoljnslxa.com", "efabnxaowuzs.com", "zaftuzsftqdq.com",
}

// decodeCFDomain mirrors _dd() from the Python version.
func decodeCFDomain(s string) string {
	if !strings.HasSuffix(s, ".com") {
		return s
	}
	p := s[:len(s)-4]
	n := 0
	for _, c := range p {
		if isAlpha(c) {
			n++
		}
	}
	var b strings.Builder
	for _, c := range p {
		if isAlpha(c) {
			base := 'a'
			if c < 'a' {
				base = 'A'
			}
			c = ((c-base-rune(n))%26+26)%26 + base
		}
		b.WriteRune(c)
	}
	return b.String() + ".co.uk"
}

func isAlpha(c rune) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

func validDomain(d string) bool {
	if d == "" || len(d) > 253 || strings.HasPrefix(d, ".") || strings.HasSuffix(d, ".") {
		return false
	}
	labels := strings.Split(d, ".")
	if len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for _, c := range l {
			if !(isAlpha(c) || (c >= '0' && c <= '9') || c == '-') {
				return false
			}
		}
	}
	tld := labels[len(labels)-1]
	hasAlpha := false
	for _, c := range tld {
		hasAlpha = hasAlpha || isAlpha(c)
	}
	return len(tld) >= 2 && hasAlpha
}

// balancer keeps the CF domain list and the last working domain per DC.
type balancer struct {
	mu      sync.Mutex
	domains []string
	perDC   map[int]string
}

func (b *balancer) setDomains(list []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(list) == 0 {
		return
	}
	same := len(list) == len(b.domains)
	if same {
		m := map[string]int{}
		for _, d := range b.domains {
			m[d]++
		}
		for _, d := range list {
			m[d]--
		}
		for _, v := range m {
			if v != 0 {
				same = false
			}
		}
	}
	if same {
		return
	}
	b.domains = append([]string(nil), list...)
	b.perDC = map[int]string{}
	for _, dc := range []int{1, 2, 3, 4, 5, 203} {
		b.perDC[dc] = b.domains[rand.IntN(len(b.domains))]
	}
}

func (b *balancer) order(dc int) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	cur := b.perDC[dc]
	out := make([]string, 0, len(b.domains))
	if cur != "" {
		out = append(out, cur)
	}
	sh := append([]string(nil), b.domains...)
	rand.Shuffle(len(sh), func(i, j int) { sh[i], sh[j] = sh[j], sh[i] })
	for _, d := range sh {
		if d != cur {
			out = append(out, d)
		}
	}
	return out
}

func (b *balancer) markGood(dc int, d string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.perDC == nil {
		b.perDC = map[int]string{}
	}
	if b.perDC[dc] == d {
		return false
	}
	b.perDC[dc] = d
	return true
}

// GitHub raw is often blocked by DNS; try a pinned IP first like Python.
var githubPinned = map[string]string{
	"raw.githubusercontent.com": "185.199.109.133",
}

func fetchCFDomains(ctx context.Context) []string {
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, _ := net.SplitHostPort(addr)
		d := net.Dialer{Timeout: 8 * time.Second}
		if ip, ok := githubPinned[host]; ok {
			if c, err := d.DialContext(ctx, network, net.JoinHostPort(ip, port)); err == nil {
				return c, nil
			}
		}
		return d.DialContext(ctx, network, addr)
	}
	cl := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext:     dial,
			TLSClientConfig: &tls.Config{},
			Proxy:           http.ProxyFromEnvironment,
		},
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", cfDomainsURL+"?"+randLetters(7), nil)
	req.Header.Set("User-Agent", "tg-ws-proxy")
	resp, err := cl.Do(req)
	if err != nil {
		Log.Warnf("Failed to fetch CF proxy domain list: %v", err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		Log.Warnf("Failed to fetch CF proxy domain list: HTTP %d", resp.StatusCode)
		return nil
	}
	var out []string
	seen := map[string]bool{}
	sc := bufio.NewScanner(io.LimitReader(resp.Body, 1<<20))
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		d := strings.ToLower(decodeCFDomain(l))
		if validDomain(d) && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

func randLetters(n int) string {
	const a = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	b := make([]byte, n)
	for i := range b {
		b[i] = a[rand.IntN(len(a))]
	}
	return string(b)
}

func defaultCFDomains() []string {
	out := make([]string, len(cfEncodedDefaults))
	for i, d := range cfEncodedDefaults {
		out[i] = decodeCFDomain(d)
	}
	return out
}
