package nextcloud

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/store"
)

var (
	mu        sync.Mutex
	pkgPool   *pgxpool.Pool
	pkgEncKey []byte
)

// Init stores the pool and encryption key for use by package-level functions.
func Init(pool *pgxpool.Pool, encKey []byte) {
	mu.Lock()
	pkgPool = pool
	pkgEncKey = encKey
	mu.Unlock()
}

// getCredentials reads Nextcloud credentials from config_store.
func getCredentials(ctx context.Context, pool *pgxpool.Pool, encKey []byte) (serverURL, username, appPassword string, err error) {
	serverURL, err = store.GetServiceConfig(ctx, pool, "nextcloud", "server_url", encKey)
	if err != nil {
		return "", "", "", fmt.Errorf("nextcloud/server_url: %w", err)
	}
	username, err = store.GetServiceConfig(ctx, pool, "nextcloud", "username", encKey)
	if err != nil {
		return "", "", "", fmt.Errorf("nextcloud/username: %w", err)
	}
	appPassword, err = store.GetServiceConfig(ctx, pool, "nextcloud", "app_password", encKey)
	if err != nil {
		return "", "", "", fmt.Errorf("nextcloud/app_password: %w", err)
	}
	serverURL = strings.TrimRight(serverURL, "/")
	return serverURL, username, appPassword, nil
}

// davURL builds the WebDAV URL for a given path.
func davURL(serverURL, username, path string) string {
	return fmt.Sprintf("%s/remote.php/dav/files/%s/%s",
		serverURL, username, strings.TrimLeft(path, "/"))
}

// EnsureFolder creates all missing path segments on Nextcloud via MKCOL.
// It iterates from the shallowest segment to the deepest, issuing one MKCOL per segment.
// MKCOL on an existing path returns 405 (Method Not Allowed), which is silently ignored.
func EnsureFolder(ctx context.Context, pool *pgxpool.Pool, encKey []byte, folderPath string) error {
	serverURL, username, appPassword, err := getCredentials(ctx, pool, encKey)
	if err != nil {
		return err
	}

	// Build all ancestor paths from shallowest to deepest.
	segments := strings.Split(strings.Trim(folderPath, "/"), "/")
	for i := range segments {
		partial := strings.Join(segments[:i+1], "/")
		url := davURL(serverURL, username, partial)
		req, err := http.NewRequestWithContext(ctx, "MKCOL", url, nil)
		if err != nil {
			return fmt.Errorf("nextcloud MKCOL %s: %w", partial, err)
		}
		req.SetBasicAuth(username, appPassword)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return fmt.Errorf("nextcloud MKCOL %s: %w", partial, err)
		}
		resp.Body.Close()

		// 201 = created, 405 = already exists — both are acceptable.
		if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusMethodNotAllowed {
			return fmt.Errorf("nextcloud MKCOL %s: unexpected status %d", partial, resp.StatusCode)
		}
	}
	return nil
}

// UploadFile streams r to Nextcloud via WebDAV PUT.
func UploadFile(ctx context.Context, pool *pgxpool.Pool, encKey []byte, folderPath, filename string, r io.Reader, contentLength int64) error {
	serverURL, username, appPassword, err := getCredentials(ctx, pool, encKey)
	if err != nil {
		return err
	}

	path := strings.TrimRight(folderPath, "/") + "/" + filename
	url := davURL(serverURL, username, path)

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, r)
	if err != nil {
		return fmt.Errorf("nextcloud PUT %s: %w", filename, err)
	}
	req.SetBasicAuth(username, appPassword)
	req.ContentLength = contentLength

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("nextcloud PUT %s: %w", filename, err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("nextcloud PUT %s: unexpected status %d", filename, resp.StatusCode)
	}
	return nil
}
