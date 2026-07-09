package fetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"syscall"

	"golang.org/x/net/html"

	"github.com/runixer/laplaced/internal/config"
)

// rawMaxBodyBytes bounds how much of a page the raw backend reads. Pages
// larger than this are truncated at the byte level before text extraction;
// the tool layer applies its own (much smaller) rune cap on the result.
const rawMaxBodyBytes = 2 << 20 // 2 MB

// rawFetcher is the no-external-service backend: plain HTTP GET plus DOM text
// extraction. No JavaScript rendering — SPAs and bot-walled sites won't read
// well; it exists so read_url works without a Firecrawl key. Extraction is
// deliberately simple (strip non-content elements, keep block structure); if
// quality ever matters, a trafilatura-style readability extractor is a known
// upgrade path.
type rawFetcher struct {
	client *http.Client
	logger *slog.Logger

	// allowPrivate permits dialing RFC1918/CGNAT/ULA addresses — intranet
	// deployments read internal wiki/docs pages. Loopback and link-local
	// stay blocked regardless (see checkIP).
	allowPrivate bool
	// allowLoopbackForTest disables the loopback guard so package tests can
	// dial httptest servers on 127.0.0.1. Never set in production wiring.
	allowLoopbackForTest bool
}

func newRawFetcher(cfg *config.FetcherConfig, logger *slog.Logger) *rawFetcher {
	f := &rawFetcher{
		logger:       logger,
		allowPrivate: cfg.AllowPrivateNetworks,
	}
	// SSRF guard lives in the dialer's Control hook: it sees the resolved IP
	// of every connection — the model picks the URL, so neither a hostname
	// resolving inward nor a redirect chain may become a door into internal
	// services. Checking post-DNS at dial time defeats DNS rebinding, and
	// each redirect hop opens a fresh connection so it is re-checked for
	// free. Clone keeps DefaultTransport's sane defaults (TLS timeouts,
	// proxy-from-env, connection pooling).
	transport := http.DefaultTransport.(*http.Transport).Clone()
	dialer := &net.Dialer{Control: f.dialControl}
	transport.DialContext = dialer.DialContext
	f.client = &http.Client{Timeout: cfg.GetTimeout(), Transport: transport}
	return f
}

// errForbiddenNetwork marks a dial refused by the SSRF guard; Fetch unwraps it
// (errors.Is walks through url.Error/net.OpError) into KindForbiddenNetwork.
var errForbiddenNetwork = errors.New("destination address is not allowed")

// cgnatNet is 100.64.0.0/10 (RFC 6598 carrier-grade NAT, also Tailscale's
// range) — net.IP.IsPrivate does not cover it.
var cgnatNet = func() *net.IPNet {
	_, n, _ := net.ParseCIDR("100.64.0.0/10")
	return n
}()

// dialControl validates the address a connection is actually being opened to.
func (f *rawFetcher) dialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("%w: unparseable dial address %q", errForbiddenNetwork, address)
	}
	return f.checkIP(ip)
}

// checkIP enforces the network policy:
//   - loopback, link-local (incl. the cloud metadata endpoint), unspecified
//     and multicast addresses are always blocked — the bot's own host is the
//     most sensitive surface (dashboard, metrics, sibling containers), and no
//     legitimate page for the model to read lives there;
//   - private ranges (RFC1918, CGNAT 100.64/10, IPv6 ULA) are blocked unless
//     fetcher.allow_private_networks is set (intranet deployments).
func (f *rawFetcher) checkIP(ip net.IP) error {
	if ip.IsLoopback() {
		if f.allowLoopbackForTest {
			return nil
		}
		return fmt.Errorf("%w: %s is a loopback address", errForbiddenNetwork, ip)
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return fmt.Errorf("%w: %s is a link-local or non-unicast address", errForbiddenNetwork, ip)
	}
	if !f.allowPrivate && (ip.IsPrivate() || cgnatNet.Contains(ip)) {
		return fmt.Errorf("%w: %s is a private network address (fetcher.allow_private_networks enables intranet fetching)", errForbiddenNetwork, ip)
	}
	return nil
}

func (f *rawFetcher) Fetch(ctx context.Context, targetURL string) (*Result, error) {
	parsed, err := url.Parse(targetURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, &Error{Kind: KindInvalidURL, Msg: fmt.Sprintf("not an http(s) URL: %q", targetURL)}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, &Error{Kind: KindInvalidURL, Msg: err.Error()}
	}
	// A browser-ish UA: many sites serve Go's default UA an error page.
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.9,*/*;q=0.5")

	resp, err := f.client.Do(req)
	if err != nil {
		if errors.Is(err, errForbiddenNetwork) {
			return nil, &Error{Kind: KindForbiddenNetwork, Msg: err.Error()}
		}
		return nil, classifyTransportError(err)
	}
	defer func() { _ = resp.Body.Close() }()

	finalURL := targetURL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}

	if resp.StatusCode >= 400 {
		return nil, &Error{
			Kind:       classifyTargetStatus(resp.StatusCode),
			StatusCode: resp.StatusCode,
			FinalURL:   finalURL,
			Msg:        fmt.Sprintf("target returned HTTP %d", resp.StatusCode),
		}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, rawMaxBodyBytes))
	if err != nil {
		return nil, classifyTransportError(err)
	}

	mediaType := ""
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		mediaType, _, _ = mime.ParseMediaType(ct)
	}

	switch {
	case mediaType == "text/html" || mediaType == "application/xhtml+xml" || mediaType == "":
		title, text := extractHTMLText(body)
		return &Result{Content: text, Title: title, FinalURL: finalURL, StatusCode: resp.StatusCode}, nil
	case strings.HasPrefix(mediaType, "text/") || mediaType == "application/json":
		return &Result{Content: string(body), FinalURL: finalURL, StatusCode: resp.StatusCode}, nil
	default:
		return nil, &Error{
			Kind:       KindUpstream,
			StatusCode: resp.StatusCode,
			FinalURL:   finalURL,
			Msg:        fmt.Sprintf("unsupported content type %q (only HTML/text/JSON readable)", mediaType),
		}
	}
}

// classifyTargetStatus maps the target page's own HTTP status (raw backend
// talks to the page directly, so 403 here means anti-bot, not policy).
func classifyTargetStatus(status int) ErrorKind {
	switch status {
	case http.StatusNotFound, http.StatusGone:
		return KindNotFound
	case http.StatusForbidden, http.StatusUnauthorized:
		return KindBlocked
	case http.StatusTooManyRequests:
		return KindRateLimited
	default:
		return KindUpstream
	}
}

// skipElements are subtrees that never carry article content.
var skipElements = map[string]bool{
	"script": true, "style": true, "noscript": true, "template": true,
	"nav": true, "header": true, "footer": true, "aside": true,
	"iframe": true, "svg": true, "form": true, "button": true,
}

// blockElements get a newline after their text so extracted content keeps
// paragraph/list structure instead of collapsing into one line.
var blockElements = map[string]bool{
	"p": true, "div": true, "br": true, "li": true, "tr": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"section": true, "article": true, "blockquote": true, "pre": true,
	"table": true, "ul": true, "ol": true,
}

var multiNewlineRE = regexp.MustCompile(`\n{3,}`)

// extractHTMLText walks the DOM, drops non-content subtrees, and returns the
// page <title> plus text with block-level line breaks preserved.
func extractHTMLText(body []byte) (title, text string) {
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		// html.Parse is extremely tolerant; on a genuine failure fall back to
		// returning nothing rather than raw markup soup.
		return "", ""
	}

	var b strings.Builder
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		switch n.Type {
		case html.ElementNode:
			if skipElements[n.Data] {
				return
			}
			if n.Data == "title" && title == "" && n.FirstChild != nil {
				title = strings.TrimSpace(n.FirstChild.Data)
				return
			}
		case html.TextNode:
			if t := strings.TrimSpace(n.Data); t != "" {
				b.WriteString(t)
				b.WriteString(" ")
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
		if n.Type == html.ElementNode && blockElements[n.Data] {
			b.WriteString("\n")
		}
	}
	walk(doc)

	text = multiNewlineRE.ReplaceAllString(b.String(), "\n\n")
	// Trim trailing spaces introduced by the word-joining writer.
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	return title, strings.TrimSpace(strings.Join(lines, "\n"))
}
