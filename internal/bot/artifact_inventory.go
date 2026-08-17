package bot

import (
	"fmt"
	"html"
	"strings"

	"github.com/runixer/laplaced/internal/files"
)

// currentArtifactInventory builds model-visible, application-authored metadata
// for files attached to the current message. The attached bytes themselves are
// already automatic generate_image inputs; IDs exist here only so the model can
// stage an exact send_artifacts selection without guessing database keys.
func currentArtifactInventory(processed []*files.ProcessedFile) (string, []int64) {
	var body strings.Builder
	ids := make([]int64, 0, len(processed))
	seen := make(map[int64]struct{}, len(processed))
	for _, file := range processed {
		if file == nil || file.ArtifactID == nil || *file.ArtifactID <= 0 {
			continue
		}
		id := *file.ArtifactID
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
		if body.Len() == 0 {
			body.WriteString("<current_artifacts generate_binding=\"automatic\">\n")
		}
		name := strings.TrimSpace(file.FileName)
		if name == "" {
			name = fmt.Sprintf("artifact-%d", id)
		}
		fmt.Fprintf(&body, "  <artifact id=\"%d\" name=\"%s\" mime=\"%s\" />\n",
			id, html.EscapeString(name), html.EscapeString(strings.TrimSpace(file.MimeType)))
	}
	if body.Len() == 0 {
		return "", nil
	}
	body.WriteString("</current_artifacts>")
	return body.String(), ids
}
