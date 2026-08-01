package web

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/jackc/pgx/v5/pgxpool"

	gauth "commons/integrations/google"
	"commons/store"
)

const googleOAuthStateCookie = "google_oauth_state"

// GoogleConnect initiates the Google OAuth flow. The admin is redirected to Google's
// consent screen requesting YouTube and Drive scopes.
func GoogleConnect(pool *pgxpool.Pool, encKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		clientID, err := store.GetServiceConfig(ctx, pool, "google", "web_client_id", encKey)
		if errors.Is(err, store.ErrNotFound) || clientID == "" {
			http.Error(w, "google/web_client_id not configured — set it in the Config section first", http.StatusUnprocessableEntity)
			return
		}
		if err != nil {
			http.Error(w, "internal error reading web_client_id", http.StatusInternalServerError)
			return
		}

		clientSecret, err := store.GetServiceConfig(ctx, pool, "google", "web_client_secret", encKey)
		if errors.Is(err, store.ErrNotFound) || clientSecret == "" {
			http.Error(w, "google/web_client_secret not configured — set it in the Config section first", http.StatusUnprocessableEntity)
			return
		}
		if err != nil {
			http.Error(w, "internal error reading web_client_secret", http.StatusInternalServerError)
			return
		}

		baseURL, err := store.BaseURL(ctx, pool, encKey)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		redirectURI := baseURL + "/auth/google/callback"

		// Generate a random CSRF state token.
		stateBytes := make([]byte, 16)
		if _, err := io.ReadFull(rand.Reader, stateBytes); err != nil {
			http.Error(w, "failed to generate state token", http.StatusInternalServerError)
			return
		}
		state := hex.EncodeToString(stateBytes)

		// Store state in a short-lived cookie for CSRF verification on callback.
		http.SetCookie(w, &http.Cookie{
			Name:     googleOAuthStateCookie,
			Value:    state,
			HttpOnly: true,
			Secure:   secureCookies,
			SameSite: http.SameSiteLaxMode,
			Path:     "/",
			MaxAge:   300, // 5 minutes
		})

		oauthCfg := &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			Endpoint:     google.Endpoint,
			Scopes:       gauth.Scopes,
			RedirectURL:  redirectURI,
		}

		params := []oauth2.AuthCodeOption{
			oauth2.AccessTypeOffline,
			oauth2.ApprovalForce, // always show consent to get a fresh refresh token
		}

		// Restrict to a specific domain if configured.
		allowedDomain, _ := store.GetServiceConfig(ctx, pool, "google", "allowed_domain", encKey)
		if allowedDomain != "" {
			params = append(params, oauth2.SetAuthURLParam("hd", allowedDomain))
		}

		authURL := oauthCfg.AuthCodeURL(state, params...)
		http.Redirect(w, r, authURL, http.StatusSeeOther)
	}
}

// GoogleCallback handles Google's OAuth redirect. It verifies the state, exchanges
// the code for a token, optionally enforces domain restriction, and stores the
// refresh token and connected email in config_store.
func GoogleCallback(pool *pgxpool.Pool, encKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		// Verify CSRF state.
		stateCookie, err := r.Cookie(googleOAuthStateCookie)
		if err != nil || stateCookie.Value == "" || stateCookie.Value != r.URL.Query().Get("state") {
			http.Error(w, "invalid OAuth state — please try again", http.StatusBadRequest)
			return
		}

		// Clear the state cookie.
		http.SetCookie(w, &http.Cookie{
			Name:     googleOAuthStateCookie,
			Value:    "",
			HttpOnly: true,
			Secure:   secureCookies,
			Path:     "/",
			MaxAge:   -1,
		})

		if errParam := r.URL.Query().Get("error"); errParam != "" {
			http.Error(w, fmt.Sprintf("Google OAuth error: %s", errParam), http.StatusBadRequest)
			return
		}

		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "missing authorization code", http.StatusBadRequest)
			return
		}

		clientID, _ := store.GetServiceConfig(ctx, pool, "google", "web_client_id", encKey)
		clientSecret, _ := store.GetServiceConfig(ctx, pool, "google", "web_client_secret", encKey)

		baseURL, err := store.BaseURL(ctx, pool, encKey)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}

		oauthCfg := &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			Endpoint:     google.Endpoint,
			Scopes:       gauth.Scopes,
			RedirectURL:  baseURL + "/auth/google/callback",
		}

		token, err := oauthCfg.Exchange(ctx, code)
		if err != nil {
			http.Error(w, fmt.Sprintf("token exchange failed: %v", err), http.StatusBadRequest)
			return
		}

		if token.RefreshToken == "" {
			http.Error(w, "no refresh token returned — revoke app access in your Google account settings and try again", http.StatusBadRequest)
			return
		}

		// Fetch the authorized account's email to enforce domain restriction and display.
		httpClient := oauthCfg.Client(ctx, token)
		email, err := fetchGoogleEmail(httpClient)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to fetch account info: %v", err), http.StatusInternalServerError)
			return
		}

		// Server-side domain enforcement.
		allowedDomain, _ := store.GetServiceConfig(ctx, pool, "google", "allowed_domain", encKey)
		if allowedDomain != "" {
			if !isAllowedDomain(email, allowedDomain) {
				http.Error(w, fmt.Sprintf("account %s is not from the allowed domain %s", email, allowedDomain), http.StatusForbidden)
				return
			}
		}

		adminID := UserIDFromContext(ctx)
		var adminIDPtr *string
		if adminID != "" {
			adminIDPtr = &adminID
		}

		if err := store.SetServiceConfig(ctx, pool, "google", "refresh_token", token.RefreshToken, true, adminIDPtr, encKey); err != nil {
			http.Error(w, fmt.Sprintf("failed to store refresh token: %v", err), http.StatusInternalServerError)
			return
		}

		// Promote the web client credentials to the main keys used by the upload pipeline.
		// This is atomic from the pipeline's perspective — any in-flight upload that already
		// read the old client_id/secret will complete normally; future uploads use the new values.
		if err := store.SetServiceConfig(ctx, pool, "google", "client_id", clientID, false, adminIDPtr, encKey); err != nil {
			http.Error(w, fmt.Sprintf("failed to update client_id: %v", err), http.StatusInternalServerError)
			return
		}
		if err := store.SetServiceConfig(ctx, pool, "google", "client_secret", clientSecret, true, adminIDPtr, encKey); err != nil {
			http.Error(w, fmt.Sprintf("failed to update client_secret: %v", err), http.StatusInternalServerError)
			return
		}

		if err := store.SetServiceConfig(ctx, pool, "google", "connected_email", email, false, adminIDPtr, encKey); err != nil {
			// Non-fatal — token is stored, just log.
			_ = err
		}

		http.Redirect(w, r, "/admin/integrations", http.StatusSeeOther)
	}
}

// fetchGoogleEmail retrieves the authenticated user's email from Google's userinfo endpoint.
func fetchGoogleEmail(client *http.Client) (string, error) {
	resp, err := client.Get("https://www.googleapis.com/oauth2/v2/userinfo")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("userinfo returned %d", resp.StatusCode)
	}

	// Parse just the email field.
	var info struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return "", fmt.Errorf("decode userinfo: %w", err)
	}
	if info.Email == "" {
		return "", fmt.Errorf("no email in userinfo response")
	}
	return info.Email, nil
}

// isAllowedDomain checks whether email belongs to the given domain.
func isAllowedDomain(email, domain string) bool {
	suffix := "@" + domain
	return len(email) > len(suffix) && email[len(email)-len(suffix):] == suffix
}
