package telegram

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
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
func NewHTTPFileDownloader(api BotAPI, fileBaseURL string) *HTTPFileDownloader {
	return &HTTPFileDownloader{
		api:         api,
		httpClient:  &http.Client{},
		fileBaseURL: fileBaseURL,
	}
}

// DownloadFile downloads a file from Telegram.
func (d *HTTPFileDownloader) DownloadFile(ctx context.Context, fileID string) ([]byte, error) {
	req := GetFileRequest{FileID: fileID}
	fileInfo, err := d.api.GetFile(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to get file info: %w", err)
	}

	token := d.api.GetToken()
	fileURL := fmt.Sprintf("%s/file/bot%s/%s", d.fileBaseURL, token, fileInfo.FilePath)

	resp, err := d.httpClient.Get(fileURL)
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
