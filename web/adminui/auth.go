package adminui

import (
	"log"
	"net/http"

	"golang.org/x/crypto/bcrypt"

	"commons/store"
	"commons/web"
	admintempl "commons/web/templ"
)

func (d Deps) LoginPage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if hasAdmin, err := store.HasAnyAdminCredentials(r.Context(), d.Pool); err == nil && !hasAdmin {
			http.Redirect(w, r, "/install", http.StatusFound)
			return
		}
		admintempl.LoginPage(r.URL.Query().Get("error") == "1").Render(r.Context(), w)
	}
}

func AdminRedirect() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/docs", http.StatusFound)
	}
}

func (d Deps) ResetPasswordPage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("token")
		if token == "" {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		_, err := store.ValidatePasswordResetToken(r.Context(), d.Pool, token)
		if err != nil {
			admintempl.ResetPasswordPage("", "This link is invalid or has expired. Ask your admin to generate a new one.").Render(r.Context(), w)
			return
		}
		admintempl.ResetPasswordPage(token, "").Render(r.Context(), w)
	}
}

func (d Deps) ResetPasswordSubmit() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		token := r.FormValue("token")
		password := r.FormValue("password")
		confirm := r.FormValue("confirm")
		if password != confirm {
			admintempl.ResetPasswordPage(token, "Passwords do not match.").Render(r.Context(), w)
			return
		}
		if len(password) < 8 {
			admintempl.ResetPasswordPage(token, "Password must be at least 8 characters.").Render(r.Context(), w)
			return
		}
		userID, err := store.ConsumePasswordResetToken(r.Context(), d.Pool, token)
		if err != nil {
			admintempl.ResetPasswordPage("", "This link is invalid or has expired. Ask your admin to generate a new one.").Render(r.Context(), w)
			return
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			log.Printf("adminui/reset-password: bcrypt error: %v", err)
			admintempl.ResetPasswordPage(token, "Something went wrong. Please try again.").Render(r.Context(), w)
			return
		}
		if err := store.SetUserPassword(r.Context(), d.Pool, userID, string(hash)); err != nil {
			log.Printf("adminui/reset-password: SetUserPassword error: %v", err)
			admintempl.ResetPasswordPage(token, "Something went wrong. Please try again.").Render(r.Context(), w)
			return
		}
		web.CreateSession(w, d.SessionSecret, userID)
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

func (d Deps) ChangePasswordPage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		admintempl.ChangePasswordPage("").Render(r.Context(), w)
	}
}

func (d Deps) ChangePasswordSubmit() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		userID := web.UserIDFromContext(ctx)
		currentPw := r.FormValue("current_password")
		newPw := r.FormValue("new_password")
		confirmPw := r.FormValue("confirm_password")
		if currentPw == "" || newPw == "" || confirmPw == "" {
			admintempl.ChangePasswordPage("All fields are required.").Render(ctx, w)
			return
		}
		if newPw != confirmPw {
			admintempl.ChangePasswordPage("New passwords do not match.").Render(ctx, w)
			return
		}
		if len(newPw) < 8 {
			admintempl.ChangePasswordPage("New password must be at least 8 characters.").Render(ctx, w)
			return
		}
		creds, err := store.GetUserCredentialsByID(ctx, d.Pool, userID)
		if err != nil {
			admintempl.ChangePasswordPage("Could not load account.").Render(ctx, w)
			return
		}
		if creds.PasswordHash == nil || bcrypt.CompareHashAndPassword([]byte(*creds.PasswordHash), []byte(currentPw)) != nil {
			admintempl.ChangePasswordPage("Current password is incorrect.").Render(ctx, w)
			return
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(newPw), bcrypt.DefaultCost)
		if err != nil {
			admintempl.ChangePasswordPage("Failed to hash password.").Render(ctx, w)
			return
		}
		if err := store.SetUserPassword(ctx, d.Pool, userID, string(hash)); err != nil {
			admintempl.ChangePasswordPage("Failed to update password.").Render(ctx, w)
			return
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}
