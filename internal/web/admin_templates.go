package web

import (
	"embed"
	"html/template"
)

//go:embed templates/admin_dashboard.html templates/admin_settings.html templates/admin_node.html templates/admin_machines.html templates/admin_machine_qr.html templates/admin_addresses.html templates/admin_deposits.html templates/admin_users.html templates/admin_user.html
var adminPageFS embed.FS

var adminPageTemplateNames = []string{
	"admin_dashboard.html",
	"admin_settings.html",
	"admin_node.html",
	"admin_machines.html",
	"admin_machine_qr.html",
	"admin_addresses.html",
	"admin_deposits.html",
	"admin_users.html",
	"admin_user.html",
}

// parseAdminPageTemplates parses the admin page set the same way
// parsePageTemplates does for the customer pages: each page is cloned from
// the shared layout so its "title"/"content" blocks don't clash with any
// other page's same-named blocks.
func parseAdminPageTemplates() (map[string]*template.Template, error) {
	base, err := template.New("layout.html").Funcs(templateFuncs).ParseFS(layoutFS, "templates/layout.html")
	if err != nil {
		return nil, err
	}

	pages := make(map[string]*template.Template, len(adminPageTemplateNames))
	for _, name := range adminPageTemplateNames {
		clone, err := base.Clone()
		if err != nil {
			return nil, err
		}
		clone, err = clone.ParseFS(adminPageFS, "templates/"+name)
		if err != nil {
			return nil, err
		}
		pages[name] = clone
	}
	return pages, nil
}
