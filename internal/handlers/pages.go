package handlers

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"path/filepath"
)

// pages.go — Phase 4a: shared header templating.
//
// PLAN2.md's Phase 4a spec calls for "Go net/html templating solely for a
// common header include." golang.org/x/net/html (the package that provides
// Go's HTML parser — there's no HTML parser in the standard library itself)
// isn't reachable from this environment: the sandbox's network allowlist
// blocks both proxy.golang.org and go.dev, so `go get` can't fetch it and
// there's no way to populate go.sum by hand. html/template — pure standard
// library, already available, and built exactly for "compose a page from a
// shared header + page-specific content" — delivers the same outcome
// without a new dependency. If golang.org/x/net/html becomes reachable
// later, swapping it in here would be a self-contained change: everything
// that composes pages lives in this one file.
//
// This handler intentionally covers only pages that have been migrated
// off their hand-duplicated header markup so far (see pageConfigs below).
// Everything else keeps going through the plain http.FileServer in
// router.go — see Handles().

// AdminNavItem is one tab in the admin section's shared nav row (rendered
// by app/partials/header-admin.html).
type AdminNavItem struct {
	Href   string
	Label  string
	Active bool
}

// adminNavItems is the full, fixed set of admin nav tabs, in display order.
// Every admin-*.html page previously hand-duplicated this same list
// (confirmed byte-identical modulo the active tab's class and one comment,
// via a diff sweep during Phase 4a); admin.html itself was the one
// exception worth double-checking, and turned out to already carry the
// same items — the shorter list seen in an earlier grep was a sed
// line-range artifact, not a real difference.
//
// Phase 4b: "Galleries" (→ gallery-manager.html, née gallery-admin.html,
// renamed "Gallery Manager" in the regular hamburger menu — see
// header-regular.html) was removed from
// this admin-section nav row on purpose. Gallery Manager isn't an
// admin-only tool — it's gated on GalleryView, not PermAdmin — so it
// doesn't belong grouped with the true admin pages; it's still reachable,
// just from the regular menu instead.
var adminNavItems = []struct {
	Key   string
	Href  string
	Label string
}{
	{"overview", "/admin-master.html", "Overview"},
	{"photos", "/admin.html", "Photos"},
	{"users", "/admin-users.html", "Users"},
	{"teams", "/admin-teams.html", "Teams"},
	{"emojis", "/admin-emojis.html", "Emojis"},
	{"labels", "/admin-labels.html", "Labels"},
	{"permissions", "/admin-permissions.html", "Permissions"},
	{"roles", "/admin-roles.html", "Roles"},
	{"templates", "/template-admin.html", "Templates"},
}

// buildAdminNav returns adminNavItems with the item whose Key matches
// activeKey marked Active, for the current page's own highlighted tab.
func buildAdminNav(activeKey string) []AdminNavItem {
	items := make([]AdminNavItem, 0, len(adminNavItems))
	for _, it := range adminNavItems {
		items = append(items, AdminNavItem{Href: it.Href, Label: it.Label, Active: it.Key == activeKey})
	}
	return items
}

// HeaderOptions controls which elements of the shared "regular" (non-admin)
// header are rendered — app/partials/header-regular.html wraps each
// toggleable element in {{if .Header.ShowX}}. PLAN2.md Phase 4a: "Regular
// header supports disabling nearly every element — used in displays to
// reduce distraction from photos."
//
// The two presets below (fullHeaderOptions / minimalHeaderOptions) capture
// index.html's and display.html's existing, hand-maintained behavior
// exactly — this phase's job was making that behavior come from one shared
// template instead of two independently-edited copies, not changing it.
// A later phase (4d, "Display Manager... gear icon for settings") is where
// this would become configurable per-display rather than fixed per-page;
// the mechanism is ready for that, it's just not wired to any UI yet.
type HeaderOptions struct {
	ShowGalleriesMenu bool // "Galleries" (+ sub-items) inside the hamburger dropdown
	ShowAdminLinks    bool // "Gallery Admin" / "Template Admin" / "Admin" links inside the hamburger dropdown
	ShowSettings      bool // settings dropdown item + gear icon + popup
	ShowUserSwitcher  bool // "act as another user" switcher (allow_multi_login users only)
	ShowUpload        bool // upload-photos button
	ShowAuthPanel     bool // profile / sign-in button + popup
}

func fullHeaderOptions() HeaderOptions {
	return HeaderOptions{
		ShowGalleriesMenu: true,
		ShowAdminLinks:    true,
		ShowSettings:      true,
		ShowUserSwitcher:  true,
		ShowUpload:        true,
		ShowAuthPanel:     true,
	}
}

// minimalHeaderOptions matches display.html's current, hand-written
// reduced header: navigation (Galleries/Gallery Admin/Template Admin/Admin
// links) stays, everything that isn't wayfinding — settings, the user
// switcher, upload, sign-in — goes.
func minimalHeaderOptions() HeaderOptions {
	return HeaderOptions{
		ShowGalleriesMenu: true,
		ShowAdminLinks:    true,
		ShowSettings:      false,
		ShowUserSwitcher:  false,
		ShowUpload:        false,
		ShowAuthPanel:     false,
	}
}

// pageData is the template data passed to every page's own top-level
// template. Not every field applies to every page: admin-style pages use
// Title/CountExpr/AdminNav (and leave Header zero-valued, unused); regular
// pages use only Header.
type pageData struct {
	Title           string
	CountExpr       string // Alpine x-text expression for the admin header's optional right-side counter; "" omits it
	AdminNav        []AdminNavItem
	HideScopePicker bool // admin-style only — see header-admin.html's doc comment
	Header          HeaderOptions
}

// pageConfig describes one templated page.
type pageConfig struct {
	file            string // basename under AppDir — also this page's html/template name
	title           string // admin-style only
	countExpr       string // admin-style only, "" = no counter
	adminNav        string // admin-style only: key into adminNavItems for the active tab
	hideScopePicker bool   // admin-style only — see header-admin.html's doc comment
	header          *HeaderOptions // regular-style only; nil means this is an admin-style page
}

// pageConfigs is the routing table for PagesHandler: which pages have been
// migrated onto the shared header partials, and how each one fills them
// in. Pages not listed here (galleries.html, photo.html, display-edit.html,
// gallery-manager.html, newdomain.html, and everything under
// photoapp_api_reference.html) keep serving as plain static files — each of
// those has real page-specific header content of its own (e.g. photo.html's
// search bar) that needs individual attention rather than a mechanical
// conversion, so they were deliberately left for a follow-up pass. See
// SUMMARIES2.md's Phase 4a entry. (template-admin.html was in this list too
// until it moved into the Admin section — see its own pageConfigs entry.)
var pageConfigs = map[string]pageConfig{
	"/admin.html":             {file: "admin.html", title: "Photo Admin", countExpr: "offset + ' of ' + total + ' photos'", adminNav: "photos"},
	"/admin-master.html":      {file: "admin-master.html", title: "Admin", adminNav: "overview"},
	"/admin-users.html":       {file: "admin-users.html", title: "User Admin", countExpr: "total + ' users'", adminNav: "users"},
	"/admin-teams.html":       {file: "admin-teams.html", title: "Team Admin", countExpr: "total + ' teams'", adminNav: "teams"},
	// adminEmojis/adminLabels never got Phase 1d's scope-picker JS wired
	// up (no scopePickerMixin() mixin, no loadAdminScope() call) — see
	// header-admin.html's doc comment. hideScopePicker keeps their
	// existing (picker-less) behavior exactly as-is.
	"/admin-emojis.html": {file: "admin-emojis.html", title: "Emoji Admin", countExpr: "total + ' emoji types'", adminNav: "emojis", hideScopePicker: true},
	"/admin-labels.html": {file: "admin-labels.html", title: "Label Admin", countExpr: "total + ' label names'", adminNav: "labels", hideScopePicker: true},
	"/admin-permissions.html": {file: "admin-permissions.html", title: "Permissions Admin", adminNav: "permissions"},
	"/admin-roles.html":       {file: "admin-roles.html", title: "Role Admin", countExpr: "total + ' roles'", adminNav: "roles"},
	// template-admin.html moved into the Admin section (PermAdmin-gated,
	// like every other page here) at the user's request — previously it
	// lived in the regular hamburger menu with its own hand-written
	// "regular" header. Its x-data component (templateAdminApp) lives in
	// app.js, not admin.js — porting its drag/resize/placard-preview logic
	// to admin.js's separate component set would be a much bigger change
	// than "move it and match the visual style", so the page keeps
	// <script src="/app.js"> and templateAdminApp; only the header/body
	// chrome around it changed. templateAdminApp has no scopePickerMixin()
	// either (same gap as adminEmojis/adminLabels), hence hideScopePicker.
	"/template-admin.html": {file: "template-admin.html", title: "Template Admin", adminNav: "templates", hideScopePicker: true},
}

func init() {
	full := fullHeaderOptions()
	minimal := minimalHeaderOptions()
	pageConfigs["/index.html"] = pageConfig{file: "index.html", header: &full}
	pageConfigs["/"] = pageConfig{file: "index.html", header: &full}
	pageConfigs["/display.html"] = pageConfig{file: "display.html", header: &minimal}
}

// PagesHandler serves the small set of pages listed in pageConfigs by
// executing a parsed html/template set (the page itself + both header
// partials). Everything else falls through to router.go's plain
// http.FileServer — see Handles.
type PagesHandler struct {
	tmpl *template.Template
}

// NewPagesHandler parses every page in pageConfigs together with
// app/partials/header-admin.html and app/partials/header-regular.html into
// one template set, once, at server startup.
func NewPagesHandler(appDir string) (*PagesHandler, error) {
	files := []string{
		filepath.Join(appDir, "partials", "header-admin.html"),
		filepath.Join(appDir, "partials", "header-regular.html"),
	}
	seen := make(map[string]bool, len(pageConfigs))
	for _, cfg := range pageConfigs {
		if seen[cfg.file] {
			continue
		}
		seen[cfg.file] = true
		files = append(files, filepath.Join(appDir, cfg.file))
	}

	tmpl, err := template.ParseFiles(files...)
	if err != nil {
		return nil, fmt.Errorf("pages: parsing templates: %w", err)
	}
	return &PagesHandler{tmpl: tmpl}, nil
}

// Handles reports whether path is one of the templated pages this handler
// serves, so router.go's fallback handler knows whether to call ServeHTTP
// here or hand the request to the static file server instead.
func (h *PagesHandler) Handles(path string) bool {
	_, ok := pageConfigs[path]
	return ok
}

func (h *PagesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cfg, ok := pageConfigs[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}

	data := pageData{Title: cfg.title, CountExpr: cfg.countExpr, HideScopePicker: cfg.hideScopePicker}
	if cfg.adminNav != "" {
		data.AdminNav = buildAdminNav(cfg.adminNav)
	}
	if cfg.header != nil {
		data.Header = *cfg.header
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tmpl.ExecuteTemplate(w, cfg.file, data); err != nil {
		// The header (and likely some of the body) may already be on the
		// wire by the time a template execution error surfaces, so there's
		// no clean way to turn this into an HTTP error response — log and
		// let the client see a truncated page, same as html/template does
		// for any other mid-stream execution failure.
		log.Printf("pages: executing %s: %v", cfg.file, err)
	}
}
