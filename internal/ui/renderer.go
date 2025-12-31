package ui

import (
	"embed"
	"fmt"
	"html/template"
	"io"
)

//go:embed templates/*.html
var templatesFS embed.FS

type Renderer struct {
	layout *template.Template
}

func NewRenderer() (*Renderer, error) {
	// Parse layout first. We use New("layout") to ensure the name is consistent if we used define.
	// But ParseFS uses filenames as names usually.
	// Our layout.html has {{define "layout"}}...{{end}}, so it defines a template named "layout".
	layout, err := template.ParseFS(templatesFS, "templates/layout.html")
	if err != nil {
		return nil, fmt.Errorf("failed to parse layout: %w", err)
	}
	return &Renderer{layout: layout}, nil
}

func (r *Renderer) Render(w io.Writer, pageTemplateFile string, data interface{}, funcMap template.FuncMap) error {
	// Clone the layout template to ensure thread safety and isolation
	tmpl, err := r.layout.Clone()
	if err != nil {
		return fmt.Errorf("failed to clone layout: %w", err)
	}

	// Register functions if provided.
	// Note: Funcs must be registered before parsing the template that uses them.
	if funcMap != nil {
		tmpl.Funcs(funcMap)
	}

	// Parse the specific page template
	_, err = tmpl.ParseFS(templatesFS, "templates/"+pageTemplateFile)
	if err != nil {
		return fmt.Errorf("failed to parse page template %s: %w", pageTemplateFile, err)
	}

	// Execute the "layout" template, which should include the "content" block defined in the page template
	return tmpl.ExecuteTemplate(w, "layout", data)
}
