package install

import (
	"net/http"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"commons/store"
	admintempl "commons/web/templ"
)

// ServeInstall mounts the install wizard routes on mux.
// Call from main.go when install mode is detected.
func ServeInstall(mux *http.ServeMux, s *State) {
	mux.HandleFunc("GET /install", handleInstallPage)
	mux.HandleFunc("POST /install/admin", func(w http.ResponseWriter, r *http.Request) {
		handleInstallAdmin(w, r, s)
	})
	mux.HandleFunc("POST /install/org", func(w http.ResponseWriter, r *http.Request) {
		handleInstallOrg(w, r, s)
	})
}

func handleInstallPage(w http.ResponseWriter, r *http.Request) {
	admintempl.InstallPage().Render(r.Context(), w)
}

func handleInstallAdmin(w http.ResponseWriter, r *http.Request, s *State) {
	displayName := strings.TrimSpace(r.FormValue("display_name"))
	email := strings.TrimSpace(r.FormValue("email"))
	password := r.FormValue("password")
	confirm := r.FormValue("password_confirm")

	if displayName == "" || email == "" || password == "" {
		admintempl.InstallStepAdmin("Display name, email, and password are required.").Render(r.Context(), w)
		return
	}
	if password != confirm {
		admintempl.InstallStepAdmin("Passwords do not match.").Render(r.Context(), w)
		return
	}
	if len(password) < 8 {
		admintempl.InstallStepAdmin("Password must be at least 8 characters.").Render(r.Context(), w)
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		admintempl.InstallStepAdmin("Failed to hash password: "+err.Error()).Render(r.Context(), w)
		return
	}

	ctx := r.Context()
	userID, err := store.UpsertBootstrapUser(ctx, s.Pool, email, string(hash))
	if err != nil {
		admintempl.InstallStepAdmin("Failed to create account: "+err.Error()).Render(r.Context(), w)
		return
	}

	if _, err := s.Pool.Exec(ctx,
		"UPDATE users SET display_name = $1 WHERE id = $2",
		displayName, userID,
	); err != nil {
		admintempl.InstallStepAdmin("Account created but failed to set display name: "+err.Error()).Render(r.Context(), w)
		return
	}

	admintempl.InstallStepOrg("").Render(r.Context(), w)
}

func handleInstallOrg(w http.ResponseWriter, r *http.Request, s *State) {
	orgName := strings.TrimSpace(r.FormValue("org_name"))
	if orgName == "" {
		admintempl.InstallStepOrg("Organization name is required.").Render(r.Context(), w)
		return
	}

	if err := store.SetServiceConfig(r.Context(), s.Pool, "ui", "org_name", orgName, false, nil, s.EncryptionKeyBytes()); err != nil {
		admintempl.InstallStepOrg("Failed to save org name: "+err.Error()).Render(r.Context(), w)
		return
	}

	admintempl.InstallStepComplete().Render(r.Context(), w)
}
