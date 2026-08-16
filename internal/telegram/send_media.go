package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

const richMessageAttachmentPrefix = "attach://"

func richMessageHasLocalAttachmentReference(message InputRichMessage) bool {
	for _, media := range message.Media {
		if strings.HasPrefix(media.Media.Media, richMessageAttachmentPrefix) {
			return true
		}
	}
	return false
}

// sendRichMessageMultipart sends one persistent rich message with local photo
// attachments. It deliberately uses the one-shot multipart helper: a network
// or decode failure after the request was written has an unknown outcome and
// must never cause an automatic duplicate send.
func (c *Client) sendRichMessageMultipart(ctx context.Context, req SendRichMessageRequest) (*Message, error) {
	fields, files, err := buildRichMessageMultipart(req)
	if err != nil {
		return nil, err
	}
	resp, err := c.makeMultipartRequest(ctx, "sendRichMessage", fields, files)
	if err != nil {
		return nil, err
	}
	return decodeSentMessage(resp, "sendRichMessage")
}

// buildRichMessageMultipart validates every local attachment before any
// network request and builds the exact multipart fields expected by Telegram.
// Local files are one-to-one with attach:// references in rich_message.media;
// remote/file_id media may coexist and require no multipart part.
func buildRichMessageMultipart(req SendRichMessageRequest) (map[string]string, []multipartFile, error) {
	mediaIDs := make(map[string]struct{}, len(req.RichMessage.Media))
	localRefs := make(map[string]struct{}, len(req.Attachments))
	for i, media := range req.RichMessage.Media {
		if !validRichMessageIdentifier(media.ID) {
			return nil, nil, fmt.Errorf("rich message media %d has invalid id %q", i, media.ID)
		}
		if _, exists := mediaIDs[media.ID]; exists {
			return nil, nil, fmt.Errorf("rich message media id %q is duplicated", media.ID)
		}
		mediaIDs[media.ID] = struct{}{}

		source := media.Media.Media
		if strings.TrimSpace(source) == "" {
			return nil, nil, fmt.Errorf("rich message media %q has an empty photo source", media.ID)
		}
		if !strings.HasPrefix(source, richMessageAttachmentPrefix) {
			continue
		}
		ref := strings.TrimPrefix(source, richMessageAttachmentPrefix)
		if !validRichMessageIdentifier(ref) {
			return nil, nil, fmt.Errorf("rich message media %q has invalid local attachment id %q", media.ID, ref)
		}
		if _, exists := localRefs[ref]; exists {
			return nil, nil, fmt.Errorf("rich message local attachment id %q is referenced more than once", ref)
		}
		localRefs[ref] = struct{}{}
	}

	attachments := make(map[string]struct{}, len(req.Attachments))
	files := make([]multipartFile, 0, len(req.Attachments))
	for i, attachment := range req.Attachments {
		if !validRichMessageIdentifier(attachment.ID) {
			return nil, nil, fmt.Errorf("rich message attachment %d has invalid id %q", i, attachment.ID)
		}
		if _, exists := attachments[attachment.ID]; exists {
			return nil, nil, fmt.Errorf("rich message attachment id %q is duplicated", attachment.ID)
		}
		attachments[attachment.ID] = struct{}{}
		if strings.TrimSpace(attachment.Filename) == "" {
			return nil, nil, fmt.Errorf("rich message attachment %q has an empty filename", attachment.ID)
		}
		if len(attachment.Data) == 0 {
			return nil, nil, fmt.Errorf("rich message attachment %q has empty data", attachment.ID)
		}
		if _, referenced := localRefs[attachment.ID]; !referenced {
			return nil, nil, fmt.Errorf("rich message attachment %q is not referenced by rich_message.media", attachment.ID)
		}
		files = append(files, multipartFile{
			FieldName: attachment.ID,
			Filename:  attachment.Filename,
			Data:      attachment.Data,
		})
	}
	for ref := range localRefs {
		if _, exists := attachments[ref]; !exists {
			return nil, nil, fmt.Errorf("rich message local attachment %q has no uploaded file", ref)
		}
	}

	richMessageJSON, err := json.Marshal(req.RichMessage)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal rich_message: %w", err)
	}
	fields := map[string]string{
		"chat_id":      strconv.FormatInt(req.ChatID, 10),
		"rich_message": string(richMessageJSON),
	}
	if req.MessageThreadID != nil {
		fields["message_thread_id"] = strconv.Itoa(*req.MessageThreadID)
	}
	if req.ReplyParameters != nil {
		replyJSON, err := json.Marshal(req.ReplyParameters)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to marshal reply_parameters: %w", err)
		}
		fields["reply_parameters"] = string(replyJSON)
	}
	return fields, files, nil
}

// Telegram requires InputRichMessageMedia identifiers to be 1-64 characters
// from this ASCII allowlist. Reusing it for multipart field identifiers keeps
// attach:// references unambiguous and safe to serialize.
func validRichMessageIdentifier(id string) bool {
	if len(id) < 1 || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

// SendPhotoRequest represents the parameters for sendPhoto.
// PhotoData holds the raw image bytes to upload; PhotoFilename is used as the
// multipart field filename (Telegram uses it for the stored photo name).
//
// NOTE: Telegram ALWAYS downscales and recompresses photos sent via sendPhoto
// (target ~1280 px on the long side). For originals > ~2 MB or higher than
// 2K resolution, use SendDocument instead to preserve quality.
type SendPhotoRequest struct {
	ChatID           int64
	MessageThreadID  *int
	PhotoData        []byte
	PhotoFilename    string // e.g. "generated.png"
	Caption          string // optional, ≤1024 UTF-16 chars
	ParseMode        string // "HTML" or "MarkdownV2" or ""
	ReplyToMessageID int
}

// SendDocumentRequest represents the parameters for sendDocument.
// Use this for images whose full resolution must be preserved (2K / 4K) —
// unlike sendPhoto, Telegram does not recompress documents.
type SendDocumentRequest struct {
	ChatID           int64
	MessageThreadID  *int
	Data             []byte
	Filename         string
	Caption          string
	ParseMode        string
	ReplyToMessageID int
}

// MediaType distinguishes media-group entry types. Telegram forbids mixing
// types within a single group.
type MediaType string

const (
	MediaTypePhoto    MediaType = "photo"
	MediaTypeDocument MediaType = "document"
)

// InputMediaPhoto is one entry of a sendMediaGroup request.
type InputMediaPhoto struct {
	// Data holds the raw image bytes; the corresponding media JSON entry will
	// reference "attach://photo_<index>".
	Data     []byte
	Filename string // e.g. "generated_1.png"
	Caption  string // optional; only the first item's caption is shown on the group
	// ParseMode applies to Caption; uses "HTML" by default when empty.
	ParseMode string
}

// SendMediaGroupRequest represents the parameters for sendMediaGroup.
// Media slice must have 2–10 entries; 1 item should use SendPhoto instead.
type SendMediaGroupRequest struct {
	ChatID           int64
	MessageThreadID  *int
	Media            []InputMediaPhoto
	ReplyToMessageID int
}

// InputMediaDocument is one entry of a document-type sendMediaGroup.
type InputMediaDocument struct {
	Data      []byte
	Filename  string
	Caption   string
	ParseMode string
}

// SendMediaGroupDocumentsRequest sends 2–10 documents as a single album.
// Telegram groups them visually while preserving full file resolution (no
// recompression), which SendMediaGroup with photos does not do.
type SendMediaGroupDocumentsRequest struct {
	ChatID           int64
	MessageThreadID  *int
	Media            []InputMediaDocument
	ReplyToMessageID int
}

// SendPhoto uploads a photo via multipart and returns the resulting message.
func (c *Client) SendPhoto(ctx context.Context, req SendPhotoRequest) (*Message, error) {
	fields := map[string]string{
		"chat_id": strconv.FormatInt(req.ChatID, 10),
	}
	if req.Caption != "" {
		fields["caption"] = req.Caption
	}
	if req.ParseMode != "" {
		fields["parse_mode"] = req.ParseMode
	}
	if req.ReplyToMessageID != 0 {
		fields["reply_to_message_id"] = strconv.Itoa(req.ReplyToMessageID)
	}
	if req.MessageThreadID != nil {
		fields["message_thread_id"] = strconv.Itoa(*req.MessageThreadID)
	}

	filename := req.PhotoFilename
	if filename == "" {
		filename = "photo.png"
	}
	files := []multipartFile{{FieldName: "photo", Filename: filename, Data: req.PhotoData}}

	resp, err := c.makeMultipartRequest(ctx, "sendPhoto", fields, files)
	if err != nil {
		return nil, err
	}

	var msg Message
	if err := json.Unmarshal(resp.Result, &msg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal sendPhoto result: %w", err)
	}
	return &msg, nil
}

// SendDocument uploads a file (preserving original bytes — no recompression)
// and returns the resulting message.
func (c *Client) SendDocument(ctx context.Context, req SendDocumentRequest) (*Message, error) {
	fields := map[string]string{
		"chat_id": strconv.FormatInt(req.ChatID, 10),
	}
	if req.Caption != "" {
		fields["caption"] = req.Caption
	}
	if req.ParseMode != "" {
		fields["parse_mode"] = req.ParseMode
	}
	if req.ReplyToMessageID != 0 {
		fields["reply_to_message_id"] = strconv.Itoa(req.ReplyToMessageID)
	}
	if req.MessageThreadID != nil {
		fields["message_thread_id"] = strconv.Itoa(*req.MessageThreadID)
	}

	filename := req.Filename
	if filename == "" {
		filename = "document.bin"
	}
	files := []multipartFile{{FieldName: "document", Filename: filename, Data: req.Data}}

	resp, err := c.makeMultipartRequest(ctx, "sendDocument", fields, files)
	if err != nil {
		return nil, err
	}

	var msg Message
	if err := json.Unmarshal(resp.Result, &msg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal sendDocument result: %w", err)
	}
	return &msg, nil
}

// SendMediaGroup uploads 2–10 photos as an album and returns the resulting
// messages (one per photo). For a single photo, use SendPhoto instead.
func (c *Client) SendMediaGroup(ctx context.Context, req SendMediaGroupRequest) ([]Message, error) {
	if len(req.Media) < 2 || len(req.Media) > 10 {
		return nil, fmt.Errorf("sendMediaGroup requires 2-10 items, got %d", len(req.Media))
	}

	mediaJSON := make([]map[string]interface{}, 0, len(req.Media))
	files := make([]multipartFile, 0, len(req.Media))
	for i, m := range req.Media {
		attachName := fmt.Sprintf("photo_%d", i)
		filename := m.Filename
		if filename == "" {
			filename = fmt.Sprintf("photo_%d.png", i)
		}
		entry := map[string]interface{}{
			"type":  "photo",
			"media": "attach://" + attachName,
		}
		if m.Caption != "" {
			entry["caption"] = m.Caption
			parseMode := m.ParseMode
			if parseMode == "" {
				parseMode = "HTML"
			}
			entry["parse_mode"] = parseMode
		}
		mediaJSON = append(mediaJSON, entry)
		files = append(files, multipartFile{FieldName: attachName, Filename: filename, Data: m.Data})
	}

	mediaBytes, err := json.Marshal(mediaJSON)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal media: %w", err)
	}

	fields := map[string]string{
		"chat_id": strconv.FormatInt(req.ChatID, 10),
		"media":   string(mediaBytes),
	}
	if req.ReplyToMessageID != 0 {
		fields["reply_to_message_id"] = strconv.Itoa(req.ReplyToMessageID)
	}
	if req.MessageThreadID != nil {
		fields["message_thread_id"] = strconv.Itoa(*req.MessageThreadID)
	}

	resp, err := c.makeMultipartRequest(ctx, "sendMediaGroup", fields, files)
	if err != nil {
		return nil, err
	}

	var msgs []Message
	if err := json.Unmarshal(resp.Result, &msgs); err != nil {
		return nil, fmt.Errorf("failed to unmarshal sendMediaGroup result: %w", err)
	}
	return msgs, nil
}

// SendMediaGroupDocuments uploads 2–10 documents as a grouped album. Unlike
// SendMediaGroup (photos), Telegram does not recompress documents.
func (c *Client) SendMediaGroupDocuments(ctx context.Context, req SendMediaGroupDocumentsRequest) ([]Message, error) {
	if len(req.Media) < 2 || len(req.Media) > 10 {
		return nil, fmt.Errorf("sendMediaGroup(documents) requires 2-10 items, got %d", len(req.Media))
	}

	mediaJSON := make([]map[string]interface{}, 0, len(req.Media))
	files := make([]multipartFile, 0, len(req.Media))
	for i, m := range req.Media {
		attachName := fmt.Sprintf("doc_%d", i)
		filename := m.Filename
		if filename == "" {
			filename = fmt.Sprintf("file_%d.bin", i)
		}
		entry := map[string]interface{}{
			"type":  "document",
			"media": "attach://" + attachName,
		}
		if m.Caption != "" {
			entry["caption"] = m.Caption
			parseMode := m.ParseMode
			if parseMode == "" {
				parseMode = "HTML"
			}
			entry["parse_mode"] = parseMode
		}
		mediaJSON = append(mediaJSON, entry)
		files = append(files, multipartFile{FieldName: attachName, Filename: filename, Data: m.Data})
	}

	mediaBytes, err := json.Marshal(mediaJSON)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal media: %w", err)
	}

	fields := map[string]string{
		"chat_id": strconv.FormatInt(req.ChatID, 10),
		"media":   string(mediaBytes),
	}
	if req.ReplyToMessageID != 0 {
		fields["reply_to_message_id"] = strconv.Itoa(req.ReplyToMessageID)
	}
	if req.MessageThreadID != nil {
		fields["message_thread_id"] = strconv.Itoa(*req.MessageThreadID)
	}

	resp, err := c.makeMultipartRequest(ctx, "sendMediaGroup", fields, files)
	if err != nil {
		return nil, err
	}
	var msgs []Message
	if err := json.Unmarshal(resp.Result, &msgs); err != nil {
		return nil, fmt.Errorf("failed to unmarshal sendMediaGroup result: %w", err)
	}
	return msgs, nil
}

// multipartFile is a file part for a multipart/form-data request.
type multipartFile struct {
	FieldName string
	Filename  string
	Data      []byte
}

// makeMultipartRequest issues a POST with multipart/form-data. Unlike
// makeRequest, it does not retry — Telegram photo/media uploads are heavier
// and retry should be decided by the caller if needed.
//
// Metrics & error sanitization mirror makeRequest where sensible.
func (c *Client) makeMultipartRequest(ctx context.Context, method string, fields map[string]string, files []multipartFile) (*APIResponse, error) {
	startTime := time.Now()

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			return nil, fmt.Errorf("failed to write field %q: %w", k, err)
		}
	}
	for _, f := range files {
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, f.FieldName, f.Filename))
		h.Set("Content-Type", "application/octet-stream")
		part, err := mw.CreatePart(h)
		if err != nil {
			return nil, fmt.Errorf("failed to create multipart part %q: %w", f.FieldName, err)
		}
		if _, err := io.Copy(part, bytes.NewReader(f.Data)); err != nil {
			return nil, fmt.Errorf("failed to write multipart body %q: %w", f.FieldName, err)
		}
	}
	if err := mw.Close(); err != nil {
		return nil, fmt.Errorf("failed to close multipart writer: %w", err)
	}

	apiURL := fmt.Sprintf("%s/%s", c.apiURL, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, &body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		duration := time.Since(startTime).Seconds()
		recordRequestDuration(method, statusError, duration)
		if isTimeoutError(err) {
			recordError(method, errorTypeTimeout)
		} else {
			recordError(method, errorTypeNetwork)
		}
		return nil, fmt.Errorf("failed to perform multipart request: %w", sanitizeError(err, c.token))
	}
	defer resp.Body.Close()

	var apiResp APIResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		duration := time.Since(startTime).Seconds()
		recordRequestDuration(method, statusError, duration)
		recordError(method, errorTypeDecode)
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	if !apiResp.Ok {
		duration := time.Since(startTime).Seconds()
		recordRequestDuration(method, statusError, duration)
		recordError(method, errorTypeAPI)
		return nil, &APIError{
			Code:        apiResp.ErrorCode,
			Description: apiResp.Description,
			Parameters:  apiResp.Parameters,
		}
	}

	duration := time.Since(startTime).Seconds()
	recordRequestDuration(method, statusSuccess, duration)
	return &apiResp, nil
}
