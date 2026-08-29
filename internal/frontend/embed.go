// Package frontend embeds the compiled web UI into the binary.
//
// The //go:embed directive packs the entire www/ subtree at compile time,
// so the binary is fully self-contained and requires no external files at
// runtime (Phase 2 ISO goal).
//
// Usage:
//
//	app.Use("/", filesystem.New(filesystem.Config{
//	    Root:  frontend.FS(),
//	    Index: "index.html",
//	}))
package frontend

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
)

//go:embed all:www all:templates
var assets embed.FS

var requiredTemplateNames = []string{
	"base",
	"first-run",
	"sidebar",
	"header",
	"firewall-banner",
	"dashboard",
	"diagnostics",
	"administration",
	"interfaces",
	"settings",
	"gateways",
	"remotes",
	"routing",
	"nat",
	"firewall-aliases",
	"firewall",
	"firewall-modals",
	"peer-modals",
	"wizards",
	"login",
	"global-modals",
	"monitoring-modal",
	"notifications",
	"firewall-toolbar",
	"separator-modal",
}

var pageTemplate = template.Must(parseTemplates(assets))

func parseTemplates(fsys fs.FS) (*template.Template, error) {
	tmpl := template.New("frontend").Delims("[[", "]]")
	// The explicit patterns keep the public www/ asset tree separate from
	// the server-side template sources.
	parsed, err := tmpl.ParseFS(fsys,
		"templates/*.html",
		"templates/components/*.html",
		"templates/views/*.html",
		"templates/modals/*.html",
	)
	if err != nil {
		return nil, err
	}
	for _, name := range requiredTemplateNames {
		if parsed.Lookup(name) == nil {
			return nil, fmt.Errorf("frontend template %q is not defined", name)
		}
	}
	return parsed, nil
}

// RenderIndex renders the complete frontend document from the embedded
// templates. Vue's {{ ... }} expressions remain literal because Go templates
// use the alternate [[ ... ]] delimiters above.
func RenderIndex() ([]byte, error) {
	var rendered bytes.Buffer
	if err := pageTemplate.ExecuteTemplate(&rendered, "base", nil); err != nil {
		return nil, err
	}
	return rendered.Bytes(), nil
}

// FS returns an http.FileSystem rooted at the embedded www/ directory.
// The "www/" prefix is stripped so files are served at their natural paths
// (e.g. "www/js/app.js" → "/js/app.js").
func FS() http.FileSystem {
	sub, err := fs.Sub(assets, "www")
	if err != nil {
		// Should never happen: "www" is always present (embed directive).
		panic("frontend: failed to sub embedded FS: " + err.Error())
	}
	return http.FS(sub)
}
