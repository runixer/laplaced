package laplace

import "regexp"

// markdownLinkRE matches a markdown inline link [text](url). The url group
// stops at the first whitespace or closing paren, matching how the renderer
// (internal/markdown) parses link destinations.
var markdownLinkRE = regexp.MustCompile(`\[([^\]]*)\]\((\S+?)\)`)

// stripUnverifiedLinks grounds source links in the model's reply against the
// set of URLs actually returned by search tools this turn. Any markdown link
// [text](url) whose url is not in seen is unwrapped to its plain text — the
// model invented or altered that URL, so we keep the words but drop the link.
// Links whose URL is verified are left untouched. Returns the cleaned reply and
// the list of stripped URLs (for the bot.anomaly.fabricated_url signal).
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
		if seen[url] {
			return match
		}
		stripped = append(stripped, url)
		return text
	})
	return cleaned, stripped
}
