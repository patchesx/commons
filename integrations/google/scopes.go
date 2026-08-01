package google

const (
	ScopeYouTubeUpload = "https://www.googleapis.com/auth/youtube.upload"
	ScopeYouTubeManage = "https://www.googleapis.com/auth/youtube"
	ScopeDrive         = "https://www.googleapis.com/auth/drive"
	ScopeEmail         = "https://www.googleapis.com/auth/userinfo.email"
)

// Scopes is the canonical set of OAuth scopes requested for all Google auth flows.
// It covers YouTube upload/management, full Drive access for recording backups, and email
// for identity verification on the OAuth callback.
var Scopes = []string{ScopeYouTubeUpload, ScopeYouTubeManage, ScopeDrive, ScopeEmail}
