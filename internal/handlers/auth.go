package handlers

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/julienschmidt/httprouter"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"golang.org/x/oauth2/facebook"

	"github.com/tjmerritt/photoapp/internal/config"
	"github.com/tjmerritt/photoapp/internal/db"
	"github.com/tjmerritt/photoapp/internal/middleware"
)

const (
	sessionCookieName = "photoapp_session"
	sessionDuration   = 30 * 24 * time.Hour
)

// AuthHandler handles all authentication routes.
type AuthHandler struct {
	DB  *db.Pool
	Cfg *config.Config
}

// ── Session helpers ───────────────────────────────────────────────────────────

func (h *AuthHandler) createSession(r *http.Request, userID string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := fmt.Sprintf("%x", raw)
	ua := r.Header.Get("User-Agent")
	ip := clientIP(r)
	_, err := h.DB.Exec(r.Context(), `
		INSERT INTO sessions (userid, token_hash, expires_at, user_agent, ip_address)
		VALUES ($1, $2, $3, $4, $5)
	`, userID, token, time.Now().Add(sessionDuration), ua, ip)
	return token, err
}

func (h *AuthHandler) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionDuration.Seconds()),
	})
}

func (h *AuthHandler) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:   sessionCookieName,
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})
}

// LookupUserFlags returns permission flags for an authenticated user.
// Called once per request after the user ID is resolved.
func (h *AuthHandler) LookupUserFlags(ctx context.Context, userID string) middleware.UserFlags {
	var flags middleware.UserFlags
	_ = h.DB.QueryRow(ctx, `
		SELECT authorized_non_public, username FROM users
		WHERE  userid = $1 AND deleted_at IS NULL
	`, userID).Scan(&flags.AuthorizedNonPublic, &flags.Username)
	return flags
}

// LookupSession returns the userID for a valid session token, or "".
func (h *AuthHandler) LookupSession(ctx context.Context, token string) string {
	var userID string
	err := h.DB.QueryRow(ctx, `
		SELECT userid::text FROM sessions
		WHERE  token_hash = $1
		  AND  revoked_at IS NULL
		  AND  expires_at > NOW()
	`, token).Scan(&userID)
	if err != nil {
		return ""
	}
	return userID
}

// finishLogin creates a session, sets the cookie, and redirects home.
func (h *AuthHandler) finishLogin(w http.ResponseWriter, r *http.Request, userID string) {
	token, err := h.createSession(r, userID)
	if err != nil {
		slog.Error("createSession failed", "error", err)
		http.Error(w, "Session creation failed", http.StatusInternalServerError)
		return
	}
	h.setSessionCookie(w, token)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// downloadExternalImage fetches an image from a remote URL, saves it to the
// upload directory, and returns the local /uploads/... path.
func (h *AuthHandler) downloadExternalImage(ctx context.Context, imageURL string) (string, error) {
	if !strings.HasPrefix(imageURL, "http://") && !strings.HasPrefix(imageURL, "https://") {
		return "", fmt.Errorf("not an HTTP/HTTPS URL")
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "PhotoApp/1.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	// Read a small header for content-type sniffing, then stream the rest.
	header := make([]byte, 512)
	n, err := io.ReadFull(resp.Body, header)
	if err != nil && err != io.ErrUnexpectedEOF {
		return "", err
	}
	header = header[:n]

	ct := http.DetectContentType(header)
	exts := map[string]string{
		"image/jpeg": ".jpg",
		"image/png":  ".png",
		"image/gif":  ".gif",
		"image/webp": ".webp",
	}
	ext, ok := exts[ct]
	if !ok {
		return "", fmt.Errorf("unsupported content type: %s", ct)
	}

	if err := os.MkdirAll(h.Cfg.UploadDir, 0o755); err != nil {
		return "", err
	}

	filename := uuid.New().String() + ext
	dest, err := os.Create(filepath.Join(h.Cfg.UploadDir, filename))
	if err != nil {
		return "", err
	}
	defer dest.Close()

	body := io.MultiReader(bytes.NewReader(header), io.LimitReader(resp.Body, 10<<20))
	if _, err := io.Copy(dest, body); err != nil {
		_ = os.Remove(filepath.Join(h.Cfg.UploadDir, filename))
		return "", err
	}

	return h.Cfg.UploadURLBase + "/" + filename, nil
}

// localizeExternalProfileImage checks whether a user's stored profile_image is
// an external URL. If it is, the image is downloaded and stored locally so the
// frontend can load it without CSP issues. Failures are logged and silently ignored.
func (h *AuthHandler) localizeExternalProfileImage(ctx context.Context, userID, externalURL string) {
	var current *string
	_ = h.DB.QueryRow(ctx,
		`SELECT profile_image FROM users WHERE userid=$1 AND deleted_at IS NULL`, userID,
	).Scan(&current)

	if current == nil || !strings.HasPrefix(*current, "https://") {
		return // already local or not set
	}

	local, err := h.downloadExternalImage(ctx, externalURL)
	if err != nil {
		slog.Warn("localizeExternalProfileImage: download failed",
			"userid", userID, "error", err)
		return
	}

	if _, err := h.DB.Exec(ctx,
		`UPDATE users SET profile_image=$1, profile_image_source=$1, updated_at=NOW() WHERE userid=$2`,
		local, userID,
	); err != nil {
		slog.Warn("localizeExternalProfileImage: DB update failed",
			"userid", userID, "error", err)
		_ = os.Remove(filepath.Join(h.Cfg.UploadDir, filepath.Base(local)))
	}
}

// findOrCreateOAuthUser looks up a user by their provider+sub, creating one if needed.
func (h *AuthHandler) findOrCreateOAuthUser(ctx context.Context, provider, sub, email, name, picture string) (string, error) {
	col := "google_id"
	switch provider {
	case "apple":
		col = "apple_id"
	case "facebook":
		col = "facebook_id"
	}

	var userID string
	err := h.DB.QueryRow(ctx,
		fmt.Sprintf(`SELECT userid::text FROM users WHERE %s = $1 AND deleted_at IS NULL`, col),
		sub,
	).Scan(&userID)
	if err == nil {
		return userID, nil
	}

	// Check if a local account exists with the same email and link it.
	if email != "" {
		err = h.DB.QueryRow(ctx,
			`SELECT userid::text FROM users WHERE email = $1 AND deleted_at IS NULL`, email,
		).Scan(&userID)
		if err == nil {
			_, err = h.DB.Exec(ctx,
				fmt.Sprintf(`UPDATE users SET %s = $1 WHERE userid = $2`, col),
				sub, userID,
			)
			return userID, err
		}
	}

	// Create a new user.
	username := usernameFromName(name, email)
	username = h.uniqueUsername(ctx, username)

	// Download the provider's picture to local storage to avoid CSP issues.
	// Fall back to a generated avatar if the download fails or no picture is provided.
	// profile_image_source tracks the real photo URL separately from the selected
	// display image (which may later be switched to a generated avatar).
	profileImage := ""
	profileImageSource := ""
	if picture != "" {
		if local, dlErr := h.downloadExternalImage(ctx, picture); dlErr == nil {
			profileImage = local
			profileImageSource = local
		} else {
			slog.Warn("findOrCreateOAuthUser: failed to download profile image",
				"error", dlErr, "url", picture)
		}
	}
	if profileImage == "" && email != "" {
		profileImage = AvatarURL(email)
	}
	var picturePtr *string
	if profileImage != "" {
		picturePtr = &profileImage
	}
	var sourcePtr *string
	if profileImageSource != "" {
		sourcePtr = &profileImageSource
	}

	err = h.DB.QueryRow(ctx, fmt.Sprintf(`
		INSERT INTO users (username, email, provider, %s, profile_image, profile_image_source)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING userid::text
	`, col), username, email, provider, sub, picturePtr, sourcePtr).Scan(&userID)
	return userID, err
}

func usernameFromName(name, email string) string {
	if name != "" {
		clean := strings.Map(func(r rune) rune {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
				return r
			}
			return '_'
		}, name)
		return strings.ToLower(strings.Trim(clean, "_"))
	}
	if email != "" {
		parts := strings.SplitN(email, "@", 2)
		return strings.ToLower(parts[0])
	}
	return "user"
}

func (h *AuthHandler) uniqueUsername(ctx context.Context, base string) string {
	candidate := base
	for i := 2; i < 1000; i++ {
		var exists bool
		_ = h.DB.QueryRow(ctx, `SELECT TRUE FROM users WHERE username = $1`, candidate).Scan(&exists)
		if !exists {
			return candidate
		}
		candidate = fmt.Sprintf("%s%d", base, i)
	}
	return base + uuid.New().String()[:6]
}

// ── GET /auth/config ──────────────────────────────────────────────────────────

// GET /auth/config — returns which OAuth providers are configured.
// Public endpoint; no authentication required.
func (h *AuthHandler) Config(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	middleware.WriteJSON(w, http.StatusOK, map[string]bool{
		"googleEnabled":   h.Cfg.GoogleClientID != "",
		"appleEnabled":    h.Cfg.AppleClientID != "",
		"facebookEnabled": h.Cfg.FacebookClientID != "",
	})
}

// ── GET /auth/me ──────────────────────────────────────────────────────────────

func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	userID, ok := middleware.UserID(r.Context())
	if !ok || userID == "" {
		middleware.WriteJSON(w, http.StatusOK, map[string]any{"loggedIn": false})
		return
	}
	var username, email, provider string
	var profileImage, profileImageSource *string
	var allowMultiLogin bool
	err := h.DB.QueryRow(r.Context(), `
		SELECT username, email,
		       COALESCE(profile_image, '/avatars/' || md5(lower(trim(COALESCE(email, userid::text))))),
		       profile_image_source,
		       provider,
		       allow_multi_login
		FROM   users WHERE userid=$1 AND deleted_at IS NULL
	`, userID).Scan(&username, &email, &profileImage, &profileImageSource, &provider, &allowMultiLogin)
	if err != nil {
		middleware.WriteJSON(w, http.StatusOK, map[string]any{"loggedIn": false})
		return
	}
	middleware.WriteJSON(w, http.StatusOK, map[string]any{
		"loggedIn":           true,
		"userid":             userID,
		"username":           username,
		"email":              email,
		"profileImage":       profileImage,
		"profileImageSource": profileImageSource,
		"provider":           provider,
		"avatarHash":         emailHash(email),
		"allowMultiLogin":    allowMultiLogin,
	})
}

// ── POST /auth/logout ─────────────────────────────────────────────────────────

func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		_, _ = h.DB.Exec(r.Context(),
			`UPDATE sessions SET revoked_at=NOW() WHERE token_hash=$1`, cookie.Value)
	}
	h.clearSessionCookie(w)
	middleware.WriteJSON(w, http.StatusOK, map[string]string{"status": "logged out"})
}

// ── Google OAuth2 ─────────────────────────────────────────────────────────────

func (h *AuthHandler) googleConfig() *oauth2.Config {
	redirectURL := h.Cfg.GoogleRedirectURL
	if redirectURL == "" {
		redirectURL = h.Cfg.BaseURL + "/auth/google/callback"
	}
	return &oauth2.Config{
		ClientID:     h.Cfg.GoogleClientID,
		ClientSecret: h.Cfg.GoogleClientSecret,
		RedirectURL:  redirectURL,
		Scopes:       []string{"openid", "email", "profile"},
		Endpoint:     google.Endpoint,
	}
}

// GET /auth/google
func (h *AuthHandler) GoogleLogin(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	if h.Cfg.GoogleClientID == "" {
		http.Error(w, "Google login not configured", http.StatusNotImplemented)
		return
	}
	state := uuid.New().String()
	http.SetCookie(w, &http.Cookie{Name: "oauth_state", Value: state, Path: "/", HttpOnly: true, MaxAge: 600})
	http.Redirect(w, r, h.googleConfig().AuthCodeURL(state, oauth2.AccessTypeOnline), http.StatusTemporaryRedirect)
}

// GET /auth/google/callback
func (h *AuthHandler) GoogleCallback(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	stateCookie, err := r.Cookie("oauth_state")
	if err != nil || stateCookie.Value != r.URL.Query().Get("state") {
		http.Error(w, "Invalid OAuth state", http.StatusBadRequest)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "oauth_state", Value: "", MaxAge: -1, Path: "/"})

	token, err := h.googleConfig().Exchange(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		slog.Error("Google token exchange failed", "error", err)
		http.Error(w, "Token exchange failed", http.StatusInternalServerError)
		return
	}

	resp, err := h.googleConfig().Client(r.Context(), token).
		Get("https://www.googleapis.com/oauth2/v3/userinfo")
	if err != nil {
		http.Error(w, "Failed to fetch user info", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var info struct {
		Sub     string `json:"sub"`
		Email   string `json:"email"`
		Name    string `json:"name"`
		Picture string `json:"picture"`
	}
	if err := json.Unmarshal(body, &info); err != nil || info.Sub == "" {
		http.Error(w, "Invalid user info from Google", http.StatusInternalServerError)
		return
	}

	userID, err := h.findOrCreateOAuthUser(r.Context(), "google", info.Sub, info.Email, info.Name, info.Picture)
	if err != nil {
		slog.Error("findOrCreateOAuthUser failed", "error", err)
		http.Error(w, "User creation failed", http.StatusInternalServerError)
		return
	}

	// For existing users who still have an external profile_image URL from before
	// this fix was deployed, download and store it locally now.
	if info.Picture != "" {
		h.localizeExternalProfileImage(r.Context(), userID, info.Picture)
	}

	h.finishLogin(w, r, userID)
}

// ── Apple Sign-In ─────────────────────────────────────────────────────────────

// GET /auth/apple
func (h *AuthHandler) AppleLogin(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	if h.Cfg.AppleClientID == "" {
		http.Error(w, "Apple login not configured", http.StatusNotImplemented)
		return
	}
	state := uuid.New().String()
	http.SetCookie(w, &http.Cookie{Name: "oauth_state", Value: state, Path: "/", HttpOnly: true, MaxAge: 600})

	redirectURL := h.Cfg.AppleRedirectURL
	if redirectURL == "" {
		redirectURL = h.Cfg.BaseURL + "/auth/apple/callback"
	}
	params := url.Values{
		"client_id":     {h.Cfg.AppleClientID},
		"redirect_uri":  {redirectURL},
		"response_type": {"code id_token"},
		"response_mode": {"form_post"},
		"scope":         {"name email"},
		"state":         {state},
	}
	http.Redirect(w, r, "https://appleid.apple.com/auth/authorize?"+params.Encode(), http.StatusTemporaryRedirect)
}

// POST /auth/apple/callback
func (h *AuthHandler) AppleCallback(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}
	stateCookie, err := r.Cookie("oauth_state")
	if err != nil || stateCookie.Value != r.FormValue("state") {
		http.Error(w, "Invalid OAuth state", http.StatusBadRequest)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "oauth_state", Value: "", MaxAge: -1, Path: "/"})

	idToken := r.FormValue("id_token")
	if idToken == "" {
		http.Error(w, "Missing id_token", http.StatusBadRequest)
		return
	}

	claims, err := validateAppleIDToken(r.Context(), idToken, h.Cfg.AppleClientID)
	if err != nil {
		slog.Error("Apple id_token validation failed", "error", err)
		http.Error(w, "Invalid Apple token", http.StatusUnauthorized)
		return
	}

	sub, _ := claims.GetSubject()
	if sub == "" {
		http.Error(w, "Missing subject", http.StatusBadRequest)
		return
	}

	email, _ := claims["email"].(string)
	var fullName string

	// Apple sends name only on first login, as a JSON form field.
	if userJSON := r.FormValue("user"); userJSON != "" {
		var u struct {
			Name  struct{ FirstName, LastName string } `json:"name"`
			Email string                               `json:"email"`
		}
		if json.Unmarshal([]byte(userJSON), &u) == nil {
			fullName = strings.TrimSpace(u.Name.FirstName + " " + u.Name.LastName)
			if email == "" {
				email = u.Email
			}
		}
	}

	userID, err := h.findOrCreateOAuthUser(r.Context(), "apple", sub, email, fullName, "")
	if err != nil {
		slog.Error("findOrCreateOAuthUser failed", "error", err)
		http.Error(w, "User creation failed", http.StatusInternalServerError)
		return
	}
	h.finishLogin(w, r, userID)
}

// validateAppleIDToken fetches Apple's JWKS and validates the JWT.
func validateAppleIDToken(ctx context.Context, tokenStr, clientID string) (jwt.MapClaims, error) {
	resp, err := http.Get("https://appleid.apple.com/auth/keys") //nolint:noctx
	if err != nil {
		return nil, fmt.Errorf("fetching Apple JWKS: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var jwks struct {
		Keys []struct {
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(body, &jwks); err != nil {
		return nil, fmt.Errorf("parsing JWKS: %w", err)
	}

	keyFunc := func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		kid, _ := t.Header["kid"].(string)
		for _, k := range jwks.Keys {
			if k.Kid == kid {
				return appleJWKToRSA(k.N, k.E)
			}
		}
		return nil, fmt.Errorf("no matching key for kid %q", kid)
	}

	parsed, err := jwt.Parse(tokenStr, keyFunc,
		jwt.WithAudience(clientID),
		jwt.WithIssuer("https://appleid.apple.com"),
	)
	if err != nil {
		return nil, err
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok || !parsed.Valid {
		return nil, fmt.Errorf("invalid token")
	}
	return claims, nil
}

func appleJWKToRSA(nB64, eB64 string) (*rsa.PublicKey, error) {
	decodeB64 := func(s string) ([]byte, error) {
		return base64.RawURLEncoding.DecodeString(s)
	}
	nBytes, err := decodeB64(nB64)
	if err != nil {
		return nil, err
	}
	eBytes, err := decodeB64(eB64)
	if err != nil {
		return nil, err
	}
	n := new(big.Int).SetBytes(nBytes)
	eInt := 0
	for _, b := range eBytes {
		eInt = eInt<<8 | int(b)
	}
	return &rsa.PublicKey{N: n, E: eInt}, nil
}

// ── Facebook Login ────────────────────────────────────────────────────────────

func (h *AuthHandler) facebookConfig() *oauth2.Config {
	redirectURL := h.Cfg.FacebookRedirectURL
	if redirectURL == "" {
		redirectURL = h.Cfg.BaseURL + "/auth/facebook/callback"
	}
	return &oauth2.Config{
		ClientID:     h.Cfg.FacebookClientID,
		ClientSecret: h.Cfg.FacebookClientSecret,
		RedirectURL:  redirectURL,
		Scopes:       []string{"public_profile", "email"},
		Endpoint:     facebook.Endpoint,
	}
}

// GET /auth/facebook
func (h *AuthHandler) FacebookLogin(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	if h.Cfg.FacebookClientID == "" {
		http.Error(w, "Facebook login not configured", http.StatusNotImplemented)
		return
	}
	state := uuid.New().String()
	http.SetCookie(w, &http.Cookie{Name: "oauth_state", Value: state, Path: "/", HttpOnly: true, MaxAge: 600})
	http.Redirect(w, r, h.facebookConfig().AuthCodeURL(state, oauth2.AccessTypeOnline), http.StatusTemporaryRedirect)
}

// GET /auth/facebook/callback
func (h *AuthHandler) FacebookCallback(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	stateCookie, err := r.Cookie("oauth_state")
	if err != nil || stateCookie.Value != r.URL.Query().Get("state") {
		http.Error(w, "Invalid OAuth state", http.StatusBadRequest)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "oauth_state", Value: "", MaxAge: -1, Path: "/"})

	token, err := h.facebookConfig().Exchange(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		slog.Error("Facebook token exchange failed", "error", err)
		http.Error(w, "Token exchange failed", http.StatusInternalServerError)
		return
	}

	resp, err := h.facebookConfig().Client(r.Context(), token).
		Get("https://graph.facebook.com/me?fields=id,name,email,picture.type(large)")
	if err != nil {
		http.Error(w, "Failed to fetch user info", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var info struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Email   string `json:"email"`
		Picture struct {
			Data struct {
				URL string `json:"url"`
			} `json:"data"`
		} `json:"picture"`
	}
	if err := json.Unmarshal(body, &info); err != nil || info.ID == "" {
		http.Error(w, "Invalid user info from Facebook", http.StatusInternalServerError)
		return
	}

	pictureURL := info.Picture.Data.URL

	userID, err := h.findOrCreateOAuthUser(r.Context(), "facebook", info.ID, info.Email, info.Name, pictureURL)
	if err != nil {
		slog.Error("findOrCreateOAuthUser failed", "error", err)
		http.Error(w, "User creation failed", http.StatusInternalServerError)
		return
	}

	if pictureURL != "" {
		h.localizeExternalProfileImage(r.Context(), userID, pictureURL)
	}

	h.finishLogin(w, r, userID)
}

// ── Local email/password auth ─────────────────────────────────────────────────

type localAuthRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

// POST /auth/register
func (h *AuthHandler) Register(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ip := clientIP(r)
	allowed, retryAfter := checkRegistrationLimit(ip)
	if !allowed {
		w.Header().Set("Retry-After", fmt.Sprintf("%.0f", retryAfter.Seconds()))
		middleware.WriteError(w, http.StatusTooManyRequests,
			fmt.Sprintf("Too many registration attempts. Try again in %.0f seconds.", retryAfter.Seconds()))
		return
	}

	var req localAuthRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.WriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	req.Username = strings.TrimSpace(req.Username)

	if req.Email == "" || req.Password == "" || req.Username == "" {
		middleware.WriteError(w, http.StatusBadRequest, "username, email and password are required")
		return
	}
	if len(req.Password) < 8 {
		middleware.WriteError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}

	// Check for existing email or username.
	var exists bool
	_ = h.DB.QueryRow(r.Context(),
		`SELECT TRUE FROM users WHERE (email=$1 OR username=$2) AND deleted_at IS NULL`,
		req.Email, req.Username,
	).Scan(&exists)
	if exists {
		middleware.WriteError(w, http.StatusConflict, "email or username already in use")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		slog.Error("Register", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "server error")
		return
	}

	var userID string
	avatarURL := AvatarURL(req.Email)
	err = h.DB.QueryRow(r.Context(), `
		INSERT INTO users (username, email, password_hash, provider, profile_image)
		VALUES ($1, $2, $3, 'local', $4)
		RETURNING userid::text
	`, req.Username, req.Email, string(hash), avatarURL).Scan(&userID)
	if err != nil {
		slog.Error("user insert failed", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "registration failed")
		return
	}

	token, err := h.createSession(r, userID)
	if err != nil {
		slog.Error("Register", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "session creation failed")
		return
	}
	h.setSessionCookie(w, token)
	middleware.WriteJSON(w, http.StatusCreated, map[string]any{
		"userid":   userID,
		"username": req.Username,
		"email":    req.Email,
	})
}

// POST /auth/login
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	var req localAuthRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.WriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))

	var userID, hash string
	err := h.DB.QueryRow(r.Context(), `
		SELECT userid::text, password_hash FROM users
		WHERE  (email=$1 OR username=$1) AND provider='local' AND deleted_at IS NULL
	`, req.Email).Scan(&userID, &hash)
	if err != nil {
		middleware.WriteError(w, http.StatusUnauthorized, "invalid email or password")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)); err != nil {
		middleware.WriteError(w, http.StatusUnauthorized, "invalid email or password")
		return
	}

	token, err := h.createSession(r, userID)
	if err != nil {
		slog.Error("Login", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "session creation failed")
		return
	}
	h.setSessionCookie(w, token)

	var username, email string
	_ = h.DB.QueryRow(r.Context(),
		`SELECT username, email FROM users WHERE userid=$1`, userID,
	).Scan(&username, &email)

	middleware.WriteJSON(w, http.StatusOK, map[string]any{
		"userid":   userID,
		"username": username,
		"email":    email,
	})
}

// ── Multi-login user list ─────────────────────────────────────────────────────

// GET /auth/users
// Returns all active (non-deleted) users for the user-switcher dropdown.
// Restricted to users whose allow_multi_login flag is set.
func (h *AuthHandler) ListUsers(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	userID, ok := middleware.UserID(r.Context())
	if !ok || userID == "" {
		middleware.WriteError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	// Verify the caller has multi-login permission.
	var allowed bool
	_ = h.DB.QueryRow(r.Context(),
		`SELECT allow_multi_login FROM users WHERE userid=$1 AND deleted_at IS NULL`, userID,
	).Scan(&allowed)
	if !allowed {
		middleware.WriteError(w, http.StatusForbidden, "multi-login not enabled for this account")
		return
	}

	rows, err := h.DB.Query(r.Context(), `
		SELECT userid::text, username,
		       COALESCE(profile_image, '/avatars/' || md5(lower(trim(COALESCE(email, userid::text)))))
		FROM   users
		WHERE  deleted_at IS NULL
		ORDER  BY username
	`)
	if err != nil {
		slog.Error("ListUsers", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	type userEntry struct {
		UserID       string `json:"userid"`
		Username     string `json:"username"`
		ProfileImage string `json:"profileImage"`
	}
	users := []userEntry{}
	for rows.Next() {
		var u userEntry
		if err := rows.Scan(&u.UserID, &u.Username, &u.ProfileImage); err != nil {
			continue
		}
		users = append(users, u)
	}
	middleware.WriteJSON(w, http.StatusOK, map[string]any{"users": users})
}

// ── Profile settings ──────────────────────────────────────────────────────────

// PATCH /auth/profile  — update profile_image to a preset avatar URL
// Body: { "profileImage": "/avatars/..." }
func (h *AuthHandler) UpdateProfile(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	userID, ok := middleware.UserID(r.Context())
	if !ok || userID == "" {
		middleware.WriteError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	var body struct {
		ProfileImage string `json:"profileImage"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		middleware.WriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	body.ProfileImage = strings.TrimSpace(body.ProfileImage)
	if body.ProfileImage == "" {
		middleware.WriteError(w, http.StatusBadRequest, "profileImage is required")
		return
	}
	// Only allow local paths: generated avatars or uploaded images.
	if !strings.HasPrefix(body.ProfileImage, "/avatars/") &&
		!strings.HasPrefix(body.ProfileImage, "/uploads/") {
		middleware.WriteError(w, http.StatusBadRequest, "invalid profileImage URL")
		return
	}
	_, err := h.DB.Exec(r.Context(),
		`UPDATE users SET profile_image=$1, updated_at=NOW() WHERE userid=$2`,
		body.ProfileImage, userID)
	if err != nil {
		slog.Error("UpdateProfile", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "db error")
		return
	}
	middleware.WriteJSON(w, http.StatusOK, map[string]string{"profileImage": body.ProfileImage})
}

// POST /auth/profile/avatar  — upload a custom profile image (local accounts only)
func (h *AuthHandler) UploadProfileAvatar(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	userID, ok := middleware.UserID(r.Context())
	if !ok || userID == "" {
		middleware.WriteError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	// Only local (email/password) accounts may upload a profile image.
	var userProvider string
	_ = h.DB.QueryRow(r.Context(),
		`SELECT provider FROM users WHERE userid=$1 AND deleted_at IS NULL`, userID,
	).Scan(&userProvider)
	if userProvider != "local" {
		middleware.WriteError(w, http.StatusForbidden, "profile image upload is only available for local accounts")
		return
	}

	if err := r.ParseMultipartForm(4 << 20); err != nil {
		middleware.WriteError(w, http.StatusBadRequest, "could not parse form (max 4MB)")
		return
	}
	file, _, err := r.FormFile("image")
	if err != nil {
		middleware.WriteError(w, http.StatusBadRequest, "image file is required")
		return
	}
	defer file.Close()

	buf := make([]byte, 512)
	n, _ := file.Read(buf)
	ct := http.DetectContentType(buf[:n])
	allowed := map[string]string{
		"image/png":  ".png",
		"image/gif":  ".gif",
		"image/webp": ".webp",
		"image/jpeg": ".jpg",
	}
	ext, ok := allowed[ct]
	if !ok {
		middleware.WriteError(w, http.StatusBadRequest, "image must be PNG, GIF, WebP or JPEG")
		return
	}

	// Reconstruct full file by prepending the already-read header bytes.
	fullFile := io.MultiReader(bytes.NewReader(buf[:n]), file)
	filename := uuid.New().String() + ext

	if err := os.MkdirAll(h.Cfg.UploadDir, 0o755); err != nil {
		slog.Error("UploadProfileAvatar", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "could not create upload directory")
		return
	}
	dest, err := os.Create(filepath.Join(h.Cfg.UploadDir, filename))
	if err != nil {
		slog.Error("UploadProfileAvatar", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "could not save file")
		return
	}
	defer dest.Close()
	if _, err := io.Copy(dest, fullFile); err != nil {
		slog.Error("UploadProfileAvatar", "error", err)
		middleware.WriteError(w, http.StatusInternalServerError, "could not save file")
		return
	}

	imageURL := h.Cfg.UploadURLBase + "/" + filename
	_, _ = h.DB.Exec(r.Context(),
		`UPDATE users SET profile_image=$1, profile_image_source=$1, updated_at=NOW() WHERE userid=$2`,
		imageURL, userID)

	middleware.WriteJSON(w, http.StatusOK, map[string]string{"profileImage": imageURL})
}
