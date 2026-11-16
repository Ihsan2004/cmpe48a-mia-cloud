// Package storage provides a thin client for Supabase Storage.
//
// Used by the batch transcription pipeline to:
//   - upload meeting audio (bot or desktop source)
//   - issue short-lived signed URLs for ElevenLabs to pull the file
//   - delete audio after successful transcription (or after retention)
//
// All calls use the service-role key, which bypasses RLS. No request here
// should ever be reachable from a browser — the handler layer enforces auth.
package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Client talks to Supabase Storage over HTTPS.
// Construct once at startup and share — http.Client is safe for concurrent use.
type Client struct {
	storageURL string // e.g. https://YOUR_PROJECT_REF.supabase.co/storage/v1
	apiKey     string // service-role key (Authorization + apikey headers both use this)
	bucket     string // logical bucket (default: meeting-audio)
	httpClient *http.Client
}

// Object represents a stored audio file reference.
type Object struct {
	Path        string // relative to bucket, e.g. "{user_id}/{meeting_id}/audio.webm"
	ContentType string
	Size        int64
}

// SignedURL is a short-lived URL that can be handed to an external service
// (e.g. ElevenLabs) to fetch the object without knowing our service key.
type SignedURL struct {
	URL       string
	ExpiresAt time.Time
}

// New returns a storage client. Returns an error if required config is missing.
func New(storageURL, serviceRoleKey, bucket string) (*Client, error) {
	if storageURL == "" {
		return nil, fmt.Errorf("storage: SUPABASE_STORAGE_URL is not set")
	}
	if serviceRoleKey == "" {
		return nil, fmt.Errorf("storage: SUPABASE_SERVICE_ROLE_KEY is not set")
	}
	if bucket == "" {
		bucket = "meeting-audio"
	}
	return &Client{
		storageURL: strings.TrimRight(storageURL, "/"),
		apiKey:     serviceRoleKey,
		bucket:     bucket,
		httpClient: &http.Client{Timeout: 10 * time.Minute},
	}, nil
}

// Bucket returns the bucket name this client is scoped to.
func (c *Client) Bucket() string { return c.bucket }

// BuildPath produces a deterministic object path for a meeting's audio.
// Format: {user_id}/{meeting_id}/audio.{ext}
// The ext is derived from contentType (audio/webm -> webm, audio/ogg -> ogg).
func BuildPath(userID, meetingID uuid.UUID, contentType string) string {
	ext := extFromContentType(contentType)
	return fmt.Sprintf("%s/%s/audio.%s", userID.String(), meetingID.String(), ext)
}

func extFromContentType(ct string) string {
	switch strings.ToLower(strings.SplitN(ct, ";", 2)[0]) {
	case "audio/webm":
		return "webm"
	case "audio/ogg":
		return "ogg"
	case "audio/mp4", "audio/m4a":
		return "m4a"
	case "audio/mpeg", "audio/mp3":
		return "mp3"
	default:
		return "bin"
	}
}

// Upload streams body to the object at path. Existing objects at the same path
// are overwritten (x-upsert=true). contentType must be a valid MIME type.
func (c *Client) Upload(ctx context.Context, path string, body io.Reader, contentType string) error {
	if path == "" {
		return fmt.Errorf("storage: path is empty")
	}
	if contentType == "" {
		return fmt.Errorf("storage: contentType is empty")
	}

	endpoint := fmt.Sprintf("%s/object/%s/%s", c.storageURL, c.bucket, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return fmt.Errorf("storage: build upload request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("apikey", c.apiKey)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("x-upsert", "true")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("storage: upload %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("storage: upload %s: status %d: %s", path, resp.StatusCode, string(msg))
	}
	return nil
}

// Download streams the object at path. Caller owns the returned ReadCloser.
func (c *Client) Download(ctx context.Context, path string) (io.ReadCloser, error) {
	endpoint := fmt.Sprintf("%s/object/%s/%s", c.storageURL, c.bucket, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("storage: build download request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("apikey", c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("storage: download %s: %w", path, err)
	}
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		resp.Body.Close()
		return nil, fmt.Errorf("storage: download %s: status %d: %s", path, resp.StatusCode, string(msg))
	}
	return resp.Body, nil
}

// Delete removes the object at path. 404 is not treated as an error
// (object may have been cleaned up already).
func (c *Client) Delete(ctx context.Context, path string) error {
	endpoint := fmt.Sprintf("%s/object/%s/%s", c.storageURL, c.bucket, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return fmt.Errorf("storage: build delete request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("apikey", c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("storage: delete %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil // already gone
	}
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("storage: delete %s: status %d: %s", path, resp.StatusCode, string(msg))
	}
	return nil
}

// ListObject is a single entry returned by List.
type ListObject struct {
	Name string
	Size int64
}

// List returns objects whose path starts with prefix, ordered by name ascending.
// Used by the upload finalize step to enumerate staging chunks in sequence.
// Supabase's list endpoint paginates; this helper fetches up to `limit` items in
// one call — pass 0 to use the backend default (100, max 1000 per Supabase docs).
func (c *Client) List(ctx context.Context, prefix string, limit int) ([]ListObject, error) {
	if limit <= 0 {
		limit = 100
	}
	endpoint := fmt.Sprintf("%s/object/list/%s", c.storageURL, c.bucket)
	body, _ := json.Marshal(map[string]any{
		"prefix": prefix,
		"limit":  limit,
		"sortBy": map[string]string{"column": "name", "order": "asc"},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("storage: build list request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("apikey", c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("storage: list %s: %w", prefix, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("storage: list %s: status %d: %s", prefix, resp.StatusCode, string(msg))
	}

	var raw []struct {
		Name     string `json:"name"`
		Metadata *struct {
			Size int64 `json:"size"`
		} `json:"metadata"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("storage: decode list response: %w", err)
	}
	out := make([]ListObject, 0, len(raw))
	for _, r := range raw {
		obj := ListObject{Name: r.Name}
		if r.Metadata != nil {
			obj.Size = r.Metadata.Size
		}
		out = append(out, obj)
	}
	return out, nil
}

// Stat returns the size of the object at path, or an error if missing.
func (c *Client) Stat(ctx context.Context, path string) (int64, error) {
	endpoint := fmt.Sprintf("%s/object/info/%s/%s", c.storageURL, c.bucket, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, fmt.Errorf("storage: build stat request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("apikey", c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("storage: stat %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return 0, fmt.Errorf("storage: stat %s: status %d: %s", path, resp.StatusCode, string(msg))
	}

	var info struct {
		Size int64 `json:"size"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return 0, fmt.Errorf("storage: decode stat response: %w", err)
	}
	return info.Size, nil
}

// CreateSignedURL issues a short-lived download URL for the object at path.
// expiresIn is in seconds (max 1 week, but keep short for audio — 15-30 min
// is typical for passing to ElevenLabs).
func (c *Client) CreateSignedURL(ctx context.Context, path string, expiresIn int) (SignedURL, error) {
	if expiresIn <= 0 {
		return SignedURL{}, fmt.Errorf("storage: expiresIn must be > 0")
	}

	endpoint := fmt.Sprintf("%s/object/sign/%s/%s", c.storageURL, c.bucket, path)
	body, _ := json.Marshal(map[string]int{"expiresIn": expiresIn})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return SignedURL{}, fmt.Errorf("storage: build sign request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("apikey", c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return SignedURL{}, fmt.Errorf("storage: sign %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return SignedURL{}, fmt.Errorf("storage: sign %s: status %d: %s", path, resp.StatusCode, string(msg))
	}

	var out struct {
		SignedURL string `json:"signedURL"` // relative path: "/object/sign/<bucket>/<path>?token=..."
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return SignedURL{}, fmt.Errorf("storage: decode sign response: %w", err)
	}

	// Supabase returns a relative URL; prepend the project URL (storageURL is ".../storage/v1").
	// Absolute URL = <origin>/storage/v1<signedURL>
	abs, err := absoluteSignedURL(c.storageURL, out.SignedURL)
	if err != nil {
		return SignedURL{}, err
	}

	return SignedURL{
		URL:       abs,
		ExpiresAt: time.Now().Add(time.Duration(expiresIn) * time.Second),
	}, nil
}

// absoluteSignedURL joins storageURL (".../storage/v1") with a relative signed path.
// Supabase returns e.g. "/object/sign/meeting-audio/abc/audio.webm?token=..."
// We need: "<origin>/storage/v1/object/sign/meeting-audio/abc/audio.webm?token=..."
func absoluteSignedURL(storageURL, relative string) (string, error) {
	u, err := url.Parse(storageURL)
	if err != nil {
		return "", fmt.Errorf("storage: parse storageURL: %w", err)
	}
	if !strings.HasPrefix(relative, "/") {
		relative = "/" + relative
	}
	// storageURL path is "/storage/v1" — keep it, append the relative path.
	u.Path = strings.TrimRight(u.Path, "/") + relative
	// The relative string already contains the query (?token=...). url.Parse
	// above stripped it from u.RawQuery because we set u.Path to the concatenated
	// string. Split explicitly to preserve.
	if idx := strings.Index(relative, "?"); idx >= 0 {
		u.Path = strings.TrimRight(storageURL, "/") + relative[:idx]
		u.RawQuery = relative[idx+1:]
		// Re-parse to normalize
		u2, err := url.Parse(u.Path + "?" + u.RawQuery)
		if err != nil {
			return "", fmt.Errorf("storage: normalize signed URL: %w", err)
		}
		return u2.String(), nil
	}
	return u.String(), nil
}
