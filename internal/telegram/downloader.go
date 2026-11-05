package telegram

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// defaultMaxFileDownloadSize is the public Bot API getFile limit.
const defaultMaxFileDownloadSize int64 = 20 * 1024 * 1024

// ErrFileDownloadTooLarge is returned when Telegram advertises or sends more
// than the active caller/per-file limit. Callers can use errors.Is to
// distinguish a permanent size rejection from a transient download failure.
var ErrFileDownloadTooLarge = errors.New("telegram file exceeds download limit")

// FileDownloader defines an interface for downloading files from Telegram.
type FileDownloader interface {
	DownloadFile(ctx context.Context, fileID string) ([]byte, error)
	// DownloadFileWithLimit applies a caller-provided byte ceiling before the
	// response is buffered. It is used by aggregate-budget schedulers, where the
	// ordinary per-file ceiling is too coarse to bound concurrent allocations.
	DownloadFileWithLimit(ctx context.Context, fileID string, maxBytes int64) ([]byte, error)
	DownloadFileAsBase64(ctx context.Context, fileID string) (string, error)
}

// HTTPFileDownloader is a concrete implementation of FileDownloader using HTTP.
type HTTPFileDownloader struct {
	api         BotAPI
	httpClient  *http.Client
	fileBaseURL string
	maxBytes    int64
	// localFilesDir enables reading the absolute file_path a --local Bot API
	// server returns; empty rejects such paths.
	localFilesDir string
}

// DownloaderOption customizes NewHTTPFileDownloader.
type DownloaderOption func(*HTTPFileDownloader)

// WithMaxDownloadBytes raises or lowers the per-file ceiling (default 20 MB,
// the public Bot API limit). Non-positive values keep the default.
func WithMaxDownloadBytes(n int64) DownloaderOption {
	return func(d *HTTPFileDownloader) {
		if n > 0 {
			d.maxBytes = n
		}
	}
}

// WithLocalFilesDir accepts absolute getFile paths under dir, which a --local
// Bot API server returns instead of a download path. The server's volume must
// be mounted at dir.
func WithLocalFilesDir(dir string) DownloaderOption {
	return func(d *HTTPFileDownloader) { d.localFilesDir = filepath.Clean(dir) }
}

// NewHTTPFileDownloader creates a new HTTPFileDownloader.
//
// HTTP client configured with:
// - 60s timeout for large file downloads
// - DisableKeepAlives to avoid connection pool issues
// - Reasonable timeouts for dial/TLS/headers
// - Proxy support (uses proxyURL if provided, otherwise falls back to environment)
func NewHTTPFileDownloader(api BotAPI, fileBaseURL, proxyURL string, opts ...DownloaderOption) (*HTTPFileDownloader, error) {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 0,
		}).DialContext,
		ForceAttemptHTTP2:     false,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		DisableKeepAlives:     true,
	}

	if proxyURL != "" {
		proxy, err := url.Parse(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("failed to parse proxy URL: %w", err)
		}
		transport.Proxy = http.ProxyURL(proxy)
	}

	d := &HTTPFileDownloader{
		api: api,
		httpClient: &http.Client{
			Timeout:   60 * time.Second, // Longer timeout for file downloads
			Transport: transport,
		},
		fileBaseURL: fileBaseURL,
		maxBytes:    defaultMaxFileDownloadSize,
	}
	for _, opt := range opts {
		opt(d)
	}
	return d, nil
}

// DownloadFile downloads a file from Telegram.
func (d *HTTPFileDownloader) DownloadFile(ctx context.Context, fileID string) ([]byte, error) {
	return d.downloadFile(ctx, fileID, d.maxBytes)
}

// DownloadFileWithLimit downloads a file while enforcing the caller's byte
// reservation before buffering the body. The configured per-file limit remains
// an upper bound even if a larger value is supplied.
func (d *HTTPFileDownloader) DownloadFileWithLimit(ctx context.Context, fileID string, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("%w: invalid byte limit %d", ErrFileDownloadTooLarge, maxBytes)
	}
	if maxBytes > d.maxBytes {
		maxBytes = d.maxBytes
	}
	return d.downloadFile(ctx, fileID, maxBytes)
}

func (d *HTTPFileDownloader) downloadFile(ctx context.Context, fileID string, maxBytes int64) ([]byte, error) {
	getFileReq := GetFileRequest{FileID: fileID}
	fileInfo, err := d.api.GetFile(ctx, getFileReq)
	if err != nil {
		return nil, fmt.Errorf("failed to get file info: %w", err)
	}

	if fileInfo.FileSize > maxBytes {
		return nil, fmt.Errorf("%w: file size %d bytes, max %d bytes", ErrFileDownloadTooLarge, fileInfo.FileSize, maxBytes)
	}
	if filepath.IsAbs(fileInfo.FilePath) {
		return d.readLocalFile(fileInfo.FilePath, maxBytes)
	}

	token := d.api.GetToken()
	fileURL := fmt.Sprintf("%s/file/bot%s/%s", d.fileBaseURL, token, fileInfo.FilePath)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := d.httpClient.Do(req)
	if err != nil {
		// Sanitize error to remove bot token from URL in error messages
		sanitized := strings.ReplaceAll(err.Error(), token, "[REDACTED]")
		return nil, fmt.Errorf("failed to download file: %s", sanitized)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to download file: status code %d", resp.StatusCode)
	}

	if resp.ContentLength > maxBytes {
		return nil, fmt.Errorf(
			"%w: content length %d bytes, max %d bytes",
			ErrFileDownloadTooLarge,
			resp.ContentLength,
			maxBytes,
		)
	}

	// Content-Length may be absent (for example for a chunked response), or it
	// may be wrong. Buffer at most maxBytes. Probe one additional byte into a
	// fixed stack buffer instead of appending it, so the returned allocation can
	// never exceed the scheduler's reservation.
	fileBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to read downloaded file: %w", err)
	}
	var extra [1]byte
	extraBytes, probeErr := resp.Body.Read(extra[:])
	if extraBytes > 0 {
		return nil, fmt.Errorf(
			"%w: received more than %d bytes",
			ErrFileDownloadTooLarge,
			maxBytes,
		)
	}
	if probeErr != nil && !errors.Is(probeErr, io.EOF) {
		return nil, fmt.Errorf("failed to probe downloaded file limit: %w", probeErr)
	}
	return fileBytes, nil
}

// readLocalFile reads the absolute file_path a --local Bot API server returns
// from its shared volume. Paths outside localFilesDir are rejected so a
// misconfigured or hostile server cannot make the bot read arbitrary files.
func (d *HTTPFileDownloader) readLocalFile(path string, maxBytes int64) ([]byte, error) {
	if d.localFilesDir == "" {
		return nil, fmt.Errorf("bot API returned a local file path, but telegram.local_mode is off")
	}
	clean := filepath.Clean(path)
	rel, err := filepath.Rel(d.localFilesDir, clean)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("bot API file path is outside %s", d.localFilesDir)
	}
	// OpenInRoot also refuses symlinks that escape the directory.
	f, err := os.OpenInRoot(d.localFilesDir, rel)
	if err != nil {
		return nil, fmt.Errorf("failed to open local bot API file: %w", err)
	}
	defer f.Close()
	if info, statErr := f.Stat(); statErr == nil && info.Size() > maxBytes {
		return nil, fmt.Errorf("%w: file size %d bytes, max %d bytes", ErrFileDownloadTooLarge, info.Size(), maxBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read local bot API file: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%w: received more than %d bytes", ErrFileDownloadTooLarge, maxBytes)
	}
	return data, nil
}

// DownloadFileAsBase64 downloads a file and encodes it as a Base64 string.
func (d *HTTPFileDownloader) DownloadFileAsBase64(ctx context.Context, fileID string) (string, error) {
	fileBytes, err := d.DownloadFile(ctx, fileID)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(fileBytes), nil
}
