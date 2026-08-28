package laplace

import (
	"net/url"
	"regexp"
	"strings"
)

// markdownLinkRE matches a markdown inline link [text](url) whose destination
// is a web URL. The url group stops at the first whitespace or closing paren,
// matching how the renderer (internal/markdown) parses link destinations.
// Only http(s) destinations are candidates for the fabricated-link guard:
// bare filenames (memory_1053_photo.jpg), anchors, and non-web schemes are
// not something a search tool could have vouched for, so flagging them only
// produces false bot.anomaly.fabricated_url signals.
var markdownLinkRE = regexp.MustCompile(`\[([^\]]*)\]\(((?i:https?)://[^)\s]*)\)`)

// stripUnverifiedLinks grounds source links in the model's reply against the
// set of URLs actually returned by search tools this turn. The sole exception
// is a canonical marketplace search URL: it contains only a user-visible text
// query and is safe to construct deterministically even when the marketplace's
// product pages are absent from the web-search index. Any other markdown link
// [text](url) whose URL is not in seen is unwrapped to its plain text — the
// model invented or altered that URL, so we keep the words but drop the link.
// Returns the cleaned reply and the list of stripped URLs (for the
// bot.anomaly.fabricated_url signal).
//
// When seen is empty (no search ran this turn) the reply is returned unchanged:
// there's nothing to verify against, and unwrapping every link would punish
// legitimate links the model may produce from other context.
func stripUnverifiedLinks(reply string, seen map[string]bool) (string, []string) {
	if len(seen) == 0 || reply == "" {
		return reply, nil
	}

	var stripped []string
	cleaned := markdownLinkRE.ReplaceAllStringFunc(reply, func(match string) string {
		m := markdownLinkRE.FindStringSubmatch(match)
		text, url := m[1], m[2]
		if seen[url] || isMarketplaceSearchURL(url) {
			return match
		}
		stripped = append(stripped, url)
		return text
	})
	return cleaned, stripped
}

// isMarketplaceSearchURL recognizes the deliberately small set of navigation
// links that the assistant may construct without claiming that a product page,
// price, rating, or availability was verified. Keep this stricter than normal
// URL validation: exact HTTPS hosts and paths, no credentials/ports/fragments,
// and exactly one non-empty query parameter.
func isMarketplaceSearchURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" {
		return false
	}

	query := u.Query()
	if len(query) != 1 {
		return false
	}

	validQuery := func(key string) bool {
		values, ok := query[key]
		return ok && len(values) == 1 && strings.TrimSpace(values[0]) != "" && len(values[0]) <= 500
	}

	switch strings.ToLower(u.Hostname()) {
	case "ozon.ru", "www.ozon.ru":
		return (u.Path == "/search" || u.Path == "/search/") && validQuery("text")
	case "wildberries.ru", "www.wildberries.ru":
		return (u.Path == "/catalog/0/search.aspx" || u.Path == "/catalog") && validQuery("search")
	case "market.yandex.ru":
		return u.Path == "/search" && validQuery("text")
	default:
		return false
	}
}
