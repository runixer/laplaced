package telegram

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// FileDownloader defines an interface for downloading files from Telegram.
type FileDownloader interface {
	DownloadFile(ctx context.Context, fileID string) ([]byte, error)
	DownloadFileAsBase64(ctx context.Context, fileID string) (string, error)
}

// HTTPFileDownloader is a concrete implementation of FileDownloader using HTTP.
type HTTPFileDownloader struct {
	api         BotAPI
	httpClient  *http.Client
	fileBaseURL string
}

// NewHTTPFileDownloader creates a new HTTPFileDownloader.
//
// HTTP client configured with:
// - 60s timeout for large file downloads
// - DisableKeepAlives to avoid connection pool issues
// - Reasonable timeouts for dial/TLS/headers
func NewHTTPFileDownloader(api BotAPI, fileBaseURL string) *HTTPFileDownloader {
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

	return &HTTPFileDownloader{
		api: api,
		httpClient: &http.Client{
			Timeout:   60 * time.Second, // Longer timeout for file downloads
			Transport: transport,
		},
		fileBaseURL: fileBaseURL,
	}
}

// DownloadFile downloads a file from Telegram.
func (d *HTTPFileDownloader) DownloadFile(ctx context.Context, fileID string) ([]byte, error) {
	getFileReq := GetFileRequest{FileID: fileID}
	fileInfo, err := d.api.GetFile(ctx, getFileReq)
	if err != nil {
		return nil, fmt.Errorf("failed to get file info: %w", err)
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

	return io.ReadAll(resp.Body)
}

// DownloadFileAsBase64 downloads a file and encodes it as a Base64 string.
func (d *HTTPFileDownloader) DownloadFileAsBase64(ctx context.Context, fileID string) (string, error) {
	fileBytes, err := d.DownloadFile(ctx, fileID)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(fileBytes), nil
}
