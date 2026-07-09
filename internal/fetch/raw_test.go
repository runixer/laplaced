package fetch

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/testutil"
)

// newLoopbackRawFetcher builds a rawFetcher that may dial httptest servers on
// 127.0.0.1 — the SSRF guard blocks loopback unconditionally in production.
func newLoopbackRawFetcher(cfg *config.FetcherConfig) *rawFetcher {
	f := newRawFetcher(cfg, testutil.TestLogger())
	f.allowLoopbackForTest = true
	return f
}

func newRawTest(t *testing.T, handler http.HandlerFunc) (*rawFetcher, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return newLoopbackRawFetcher(&config.FetcherConfig{Backend: "raw"}), srv
}

func TestRawFetch_HTMLExtraction(t *testing.T) {
	page := `<!DOCTYPE html><html><head>
		<title>Test Page</title>
		<style>body { color: red }</style>
		<script>alert("nope")</script>
	</head><body>
		<nav>Menu Home About</nav>
		<article><h1>Heading</h1><p>First paragraph.</p><p>Second paragraph.</p></article>
		<footer>copyright</footer>
	</body></html>`
	f, srv := newRawTest(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, page)
	})

	res, err := f.Fetch(context.Background(), srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "Test Page", res.Title)
	assert.Contains(t, res.Content, "Heading")
	assert.Contains(t, res.Content, "First paragraph.")
	assert.Contains(t, res.Content, "Second paragraph.")
	assert.NotContains(t, res.Content, "alert")
	assert.NotContains(t, res.Content, "color: red")
	assert.NotContains(t, res.Content, "Menu Home")
	assert.NotContains(t, res.Content, "copyright")
	// Block elements keep paragraphs on separate lines.
	assert.Contains(t, res.Content, "First paragraph.\nSecond paragraph.")
}

func TestRawFetch_Redirect(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/short", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/final-page", http.StatusFound)
	})
	mux.HandleFunc("/final-page", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html><head><title>Final</title></head><body><p>ok</p></body></html>")
	})
	f := newLoopbackRawFetcher(&config.FetcherConfig{Backend: "raw"})

	res, err := f.Fetch(context.Background(), srv.URL+"/short")
	require.NoError(t, err)
	assert.Equal(t, srv.URL+"/final-page", res.FinalURL)
	assert.Equal(t, "Final", res.Title)
}

func TestRawFetch_StatusMapping(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		wantKind ErrorKind
	}{
		{"404", http.StatusNotFound, KindNotFound},
		{"410", http.StatusGone, KindNotFound},
		{"403 anti-bot", http.StatusForbidden, KindBlocked},
		{"401", http.StatusUnauthorized, KindBlocked},
		{"429", http.StatusTooManyRequests, KindRateLimited},
		{"503", http.StatusServiceUnavailable, KindUpstream},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
			}))
			t.Cleanup(srv.Close)
			f := newLoopbackRawFetcher(&config.FetcherConfig{Backend: "raw"})

			_, err := f.Fetch(context.Background(), srv.URL)
			var fErr *Error
			require.ErrorAs(t, err, &fErr)
			assert.Equal(t, tt.wantKind, fErr.Kind)
			assert.Equal(t, tt.status, fErr.StatusCode)
			assert.Equal(t, srv.URL, fErr.FinalURL)
		})
	}
}

func TestRawFetch_RedirectThenBlocked_ReportsFinalURL(t *testing.T) {
	// The shortlink case: redirect resolves, final page is a captcha wall.
	// The resolved URL must survive in the error.
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/t/abc", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/product/super-iron-9000", http.StatusFound)
	})
	mux.HandleFunc("/product/super-iron-9000", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	f := newLoopbackRawFetcher(&config.FetcherConfig{Backend: "raw"})

	_, err := f.Fetch(context.Background(), srv.URL+"/t/abc")
	var fErr *Error
	require.ErrorAs(t, err, &fErr)
	assert.Equal(t, KindBlocked, fErr.Kind)
	assert.Equal(t, srv.URL+"/product/super-iron-9000", fErr.FinalURL)
}

func TestRawFetch_PlainTextPassthrough(t *testing.T) {
	f, srv := newRawTest(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, "just plain text")
	})

	res, err := f.Fetch(context.Background(), srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "just plain text", res.Content)
	assert.Empty(t, res.Title)
}

func TestRawFetch_JSONPassthrough(t *testing.T) {
	f, srv := newRawTest(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"key":"value"}`)
	})

	res, err := f.Fetch(context.Background(), srv.URL)
	require.NoError(t, err)
	assert.Equal(t, `{"key":"value"}`, res.Content)
}

func TestRawFetch_UnsupportedContentType(t *testing.T) {
	f, srv := newRawTest(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		fmt.Fprint(w, "%PDF-1.4 binary stuff")
	})

	_, err := f.Fetch(context.Background(), srv.URL)
	var fErr *Error
	require.ErrorAs(t, err, &fErr)
	assert.Equal(t, KindUpstream, fErr.Kind)
	assert.Contains(t, fErr.Msg, "unsupported content type")
}

func TestRawFetch_InvalidURL(t *testing.T) {
	f := newLoopbackRawFetcher(&config.FetcherConfig{Backend: "raw"})
	tests := []string{"ftp://example.com/file", "not a url", "file:///etc/passwd", ""}
	for _, u := range tests {
		t.Run(u, func(t *testing.T) {
			_, err := f.Fetch(context.Background(), u)
			var fErr *Error
			require.ErrorAs(t, err, &fErr)
			assert.Equal(t, KindInvalidURL, fErr.Kind)
		})
	}
}

func TestRawFetch_BodySizeLimit(t *testing.T) {
	// 3 MB body: extraction must succeed on the first 2 MB, not blow up.
	f, srv := newRawTest(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, strings.Repeat("a", 3<<20))
	})

	res, err := f.Fetch(context.Background(), srv.URL)
	require.NoError(t, err)
	assert.Len(t, res.Content, 2<<20)
}

func TestExtractHTMLText_Empty(t *testing.T) {
	title, text := extractHTMLText(nil)
	assert.Empty(t, title)
	assert.Empty(t, text)
}

// TestRawFetch_CheckIP pins the SSRF network policy: loopback/link-local
// (incl. the cloud metadata endpoint) are refused unconditionally; private
// ranges are gated on allow_private_networks; public addresses always pass.
func TestRawFetch_CheckIP(t *testing.T) {
	tests := []struct {
		name         string
		ip           string
		allowPrivate bool
		wantBlocked  bool
	}{
		{"public v4", "93.184.216.34", false, false},
		{"public v6", "2606:2800:220:1:248:1893:25c8:1946", false, false},
		{"loopback", "127.0.0.1", false, true},
		{"loopback v6", "::1", false, true},
		{"loopback stays blocked with allow_private", "127.0.0.1", true, true},
		{"cloud metadata stays blocked with allow_private", "169.254.169.254", true, true},
		{"link-local v6", "fe80::1", false, true},
		{"unspecified", "0.0.0.0", false, true},
		{"rfc1918 10/8", "10.1.2.3", false, true},
		{"rfc1918 172.16/12", "172.20.0.5", false, true},
		{"rfc1918 192.168/16", "192.168.1.10", false, true},
		{"cgnat (tailscale)", "100.100.1.1", false, true},
		{"ipv6 ula", "fd12:3456::1", false, true},
		{"rfc1918 allowed on intranet", "192.168.1.10", true, false},
		{"cgnat allowed on intranet", "100.100.1.1", true, false},
		{"ula allowed on intranet", "fd12:3456::1", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newRawFetcher(&config.FetcherConfig{Backend: "raw", AllowPrivateNetworks: tt.allowPrivate}, testutil.TestLogger())
			err := f.checkIP(net.ParseIP(tt.ip))
			if tt.wantBlocked {
				require.ErrorIs(t, err, errForbiddenNetwork)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestRawFetch_ForbiddenNetwork_EndToEnd drives the guard through a real
// dial: without the test override, a fetch to a loopback httptest server must
// classify as KindForbiddenNetwork (the guard fires in the dialer, so DNS
// tricks and redirect hops go through the same check).
func TestRawFetch_ForbiddenNetwork_EndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("should never be reached"))
	}))
	t.Cleanup(srv.Close)

	f := newRawFetcher(&config.FetcherConfig{Backend: "raw"}, testutil.TestLogger())
	_, err := f.Fetch(context.Background(), srv.URL)
	var fErr *Error
	require.ErrorAs(t, err, &fErr)
	assert.Equal(t, KindForbiddenNetwork, fErr.Kind)
}
