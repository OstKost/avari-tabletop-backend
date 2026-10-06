package handler

import (
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
)

type Renderer struct {
	templates map[string]*template.Template
	fs        fs.FS
}

func NewRenderer(webFS fs.FS) (*Renderer, error) {
	r := &Renderer{
		templates: make(map[string]*template.Template),
		fs:        webFS,
	}

	// Base files included in every full-page template
	base := []string{
		"templates/layouts/base.html",
		"templates/partials/nav.html",
		"templates/partials/toast.html",
	}

	// Full pages: map[name][]extra_partials_needed_by_page
	type pageDef struct {
		name     string
		includes []string // additional partials this page calls via {{template}}
	}
	pages := []pageDef{
		{name: "home"},
		{name: "login"},
		{name: "register"},
		{name: "error"},
		{name: "chat", includes: []string{"templates/partials/chat_message.html"}},
		{name: "game_detail", includes: []string{"templates/partials/add_button.html"}},
		{name: "rules_upload"},
		// search.html calls {{template "search_results"}} which calls {{template "add_button"}}
		{name: "search", includes: []string{
			"templates/partials/search_results.html",
			"templates/partials/add_button.html",
		}},
		// collection.html calls collection_list which calls collection_card
		{name: "collection", includes: []string{
			"templates/partials/collection_list.html",
			"templates/partials/collection_card.html",
		}},
	}

	for _, p := range pages {
		files := append(base, "templates/pages/"+p.name+".html")
		files = append(files, p.includes...)
		tmpl, err := template.New("base").Funcs(templateFuncs()).ParseFS(webFS, files...)
		if err != nil {
			return nil, err
		}
		r.templates[p.name] = tmpl
	}

	// Standalone partial templates for HTMX swap responses.
	// A partial may itself depend on other partials — list all needed files.
	type partialDef struct {
		name  string
		files []string
	}
	partials := []partialDef{
		{name: "toast", files: []string{"templates/partials/toast.html"}},
		// search_results calls {{template "add_button"}}
		{name: "search_results", files: []string{
			"templates/partials/search_results.html",
			"templates/partials/add_button.html",
		}},
		{name: "add_button", files: []string{"templates/partials/add_button.html"}},
		{name: "chat_message", files: []string{"templates/partials/chat_message.html"}},
		// collection_list calls collection_card — must parse both together
		{name: "collection_list", files: []string{
			"templates/partials/collection_list.html",
			"templates/partials/collection_card.html",
		}},
		{name: "collection_card", files: []string{"templates/partials/collection_card.html"}},
	}

	for _, p := range partials {
		tmpl, err := template.New(p.name).Funcs(templateFuncs()).ParseFS(webFS, p.files...)
		if err != nil {
			return nil, err
		}
		r.templates["partial/"+p.name] = tmpl
	}

	return r, nil
}

func (r *Renderer) Render(w http.ResponseWriter, name string, data any) {
	tmpl, ok := r.templates[name]
	if !ok {
		slog.Error("template not found", "name", name)
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "base", data); err != nil {
		slog.Error("render template", "name", name, "error", err)
	}
}

func (r *Renderer) RenderPartial(w http.ResponseWriter, name string, data any) {
	tmpl, ok := r.templates["partial/"+name]
	if !ok {
		slog.Error("partial template not found", "name", name)
		http.Error(w, "partial not found", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, name, data); err != nil {
		slog.Error("render partial", "name", name, "error", err)
	}
}

func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"add": func(a, b int) int { return a + b },
		"seq": func(n int) []int {
			s := make([]int, n)
			for i := range s {
				s[i] = i + 1
			}
			return s
		},
		"deref": func(p *int) int {
			if p == nil {
				return 0
			}
			return *p
		},
	}
}
