package web

import (
	"embed"
	"html/template"
)

//go:embed templates/layout.html
var layoutFS embed.FS

//go:embed templates/index.html templates/buy.html templates/machines.html templates/machine.html templates/history.html
var pageFS embed.FS

// pageTemplateNames are the content templates rendered inside the shared
// layout. Each is parsed together with layout.html so it can override the
// layout's "title"/"content" blocks without the page templates' own
// same-named blocks clashing with each other.
var pageTemplateNames = []string{
	"index.html",
	"buy.html",
	"machines.html",
	"machine.html",
	"history.html",
}

// templateFuncs are available to every page template (customer and admin).
var templateFuncs = template.FuncMap{
	"doge": formatKoinuAsDoge,
}

func parsePageTemplates() (map[string]*template.Template, error) {
	base, err := template.New("layout.html").Funcs(templateFuncs).ParseFS(layoutFS, "templates/layout.html")
	if err != nil {
		return nil, err
	}

	pages := make(map[string]*template.Template, len(pageTemplateNames))
	for _, name := range pageTemplateNames {
		clone, err := base.Clone()
		if err != nil {
			return nil, err
		}
		clone, err = clone.ParseFS(pageFS, "templates/"+name)
		if err != nil {
			return nil, err
		}
		pages[name] = clone
	}
	return pages, nil
}
