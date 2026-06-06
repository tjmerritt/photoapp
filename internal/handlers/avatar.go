package handlers

import (
	"crypto/md5"
	"fmt"
	"net/http"
	"strings"

	"codeberg.org/Codeberg/avatars"
	"github.com/julienschmidt/httprouter"
)

// AvatarURL returns the relative URL for a generated avatar given an email address.
// The same email always produces the same URL (and thus the same image).
func AvatarURL(email string) string {
	hash := emailHash(email)
	return "/avatars/" + hash
}

// emailHash returns the lowercase MD5 hex of the trimmed, lowercased email.
func emailHash(email string) string {
	h := md5.Sum([]byte(strings.ToLower(strings.TrimSpace(email))))
	return fmt.Sprintf("%x", h)
}

// GET /avatars/:hash
// Generates and serves a deterministic SVG avatar for the given hash.
// The hash is typically MD5(lowercase email), matching the AvatarURL helper.
func ServeAvatar(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	hash := ps.ByName("hash")
	if hash == "" {
		http.NotFound(w, r)
		return
	}
	svg := avatars.MakeAvatar(hash)
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	_, _ = fmt.Fprint(w, svg)
}
