package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildRichPhotoMedia_DeterministicBindings(t *testing.T) {
	for _, count := range []int{1, 2, 4, 10} {
		t.Run(fmt.Sprintf("photos_%d", count), func(t *testing.T) {
			uploads := makeRichPhotoUploads(count)

			media, attachments, err := BuildRichPhotoMedia(uploads)

			require.NoError(t, err)
			require.Len(t, media, count)
			require.Len(t, attachments, count)
			for i := 0; i < count; i++ {
				mediaID := fmt.Sprintf("rich_photo_%d", i)
				attachmentID := fmt.Sprintf("rich_photo_%d_file", i)
				assert.Equal(t, mediaID, media[i].ID)
				assert.Equal(t, "attach://"+attachmentID, media[i].Media.Media)
				assert.Equal(t, attachmentID, attachments[i].ID)
				assert.Equal(t, uploads[i].Filename, attachments[i].Filename)
				assert.Equal(t, uploads[i].MIME, attachments[i].ContentType)
				assert.Equal(t, uploads[i].Data, attachments[i].Data)
				assert.True(t, validRichMessageIdentifier(media[i].ID))
				assert.True(t, validRichMessageIdentifier(attachments[i].ID))
			}
		})
	}
}

func TestBuildRichPhotoMedia_RejectsInvalidUploads(t *testing.T) {
	tests := []struct {
		name    string
		uploads []RichPhotoUpload
		want    string
	}{
		{name: "empty", want: "requires 1-50"},
		{name: "too many", uploads: makeRichPhotoUploads(MaxRichMessageMedia + 1), want: "requires 1-50"},
		{name: "empty filename", uploads: []RichPhotoUpload{{MIME: "image/png", Data: []byte("png")}}, want: "empty filename"},
		{name: "unsafe filename", uploads: []RichPhotoUpload{{Filename: "bad\r\nname.png", MIME: "image/png", Data: []byte("png")}}, want: "control characters"},
		{name: "empty data", uploads: []RichPhotoUpload{{Filename: "photo.png", MIME: "image/png"}}, want: "empty data"},
		{name: "empty mime", uploads: []RichPhotoUpload{{Filename: "photo.png", Data: []byte("png")}}, want: "not an image"},
		{name: "invalid mime", uploads: []RichPhotoUpload{{Filename: "photo.png", MIME: "image/[png", Data: []byte("png")}}, want: "invalid content type"},
		{name: "non image mime", uploads: []RichPhotoUpload{{Filename: "photo.png", MIME: "text/plain", Data: []byte("png")}}, want: "not an image"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			media, attachments, err := BuildRichPhotoMedia(tt.uploads)
			require.Error(t, err)
			assert.Nil(t, media)
			assert.Nil(t, attachments)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestSendRichMessage_MultipartPhotoCountsUseOneRequest(t *testing.T) {
	for _, count := range []int{1, 2, 4, 10} {
		t.Run(fmt.Sprintf("photos_%d", count), func(t *testing.T) {
			uploads := makeRichPhotoUploads(count)
			media, attachments, err := BuildRichPhotoMedia(uploads)
			require.NoError(t, err)
			richHTML := richPhotoTestHTML(media)
			calls := 0

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				assert.Equal(t, "/botfake-token/sendRichMessage", r.URL.Path)
				require.NoError(t, r.ParseMultipartForm(2<<20))
				require.NotNil(t, r.MultipartForm)
				require.Len(t, r.MultipartForm.Value["rich_message"], 1)

				var richMessage struct {
					HTML  string `json:"html"`
					Media []struct {
						ID    string `json:"id"`
						Media struct {
							Type   string `json:"type"`
							Source string `json:"media"`
						} `json:"media"`
					} `json:"media"`
				}
				require.NoError(t, json.Unmarshal([]byte(r.MultipartForm.Value["rich_message"][0]), &richMessage))
				assert.Equal(t, richHTML, richMessage.HTML)
				require.Len(t, richMessage.Media, count)
				require.Len(t, r.MultipartForm.File, count)
				for i := 0; i < count; i++ {
					attachmentID := fmt.Sprintf("rich_photo_%d_file", i)
					assert.Equal(t, fmt.Sprintf("rich_photo_%d", i), richMessage.Media[i].ID)
					assert.Equal(t, "photo", richMessage.Media[i].Media.Type)
					assert.Equal(t, "attach://"+attachmentID, richMessage.Media[i].Media.Source)

					files := r.MultipartForm.File[attachmentID]
					require.Len(t, files, 1)
					assert.Equal(t, uploads[i].Filename, files[0].Filename)
					assert.Equal(t, uploads[i].MIME, files[0].Header.Get("Content-Type"))
					file, err := files[0].Open()
					require.NoError(t, err)
					got, err := io.ReadAll(file)
					require.NoError(t, err)
					require.NoError(t, file.Close())
					assert.Equal(t, uploads[i].Data, got)
				}

				require.NoError(t, json.NewEncoder(w).Encode(APIResponse{
					Ok: true, Result: json.RawMessage(`{"message_id":88,"chat":{"id":123,"type":"private"}}`),
				}))
			}))
			defer server.Close()

			client := &Client{token: "fake-token", httpClient: server.Client(), apiURL: server.URL + "/botfake-token"}
			message, err := client.SendRichMessage(context.Background(), SendRichMessageRequest{
				ChatID: 123,
				RichMessage: InputRichMessage{
					HTML: richHTML, Media: media, SkipEntityDetection: true,
				},
				Attachments: attachments,
			})

			require.NoError(t, err)
			require.NotNil(t, message)
			assert.Equal(t, 88, message.MessageID)
			assert.Equal(t, 1, calls, "one persistent operation must issue one Bot API request")
		})
	}
}

func TestSendRichMessage_RejectsDanglingAndDuplicatePhotoBindingsBeforeRequest(t *testing.T) {
	valid := func() SendRichMessageRequest {
		media, attachments, err := BuildRichPhotoMedia(makeRichPhotoUploads(2))
		require.NoError(t, err)
		return SendRichMessageRequest{
			ChatID:      123,
			RichMessage: InputRichMessage{HTML: richPhotoTestHTML(media), Media: media},
			Attachments: attachments,
		}
	}
	tests := []struct {
		name   string
		mutate func(*SendRichMessageRequest)
		want   string
	}{
		{name: "html reference without media", mutate: func(req *SendRichMessageRequest) {
			req.RichMessage.HTML += `<img src="tg://photo?id=missing"/>`
		}, want: "has no rich message media entry"},
		{name: "media without html reference", mutate: func(req *SendRichMessageRequest) {
			req.RichMessage.HTML = `<img src="tg://photo?id=rich_photo_0"/>`
		}, want: "is not referenced"},
		{name: "duplicate html reference", mutate: func(req *SendRichMessageRequest) {
			req.RichMessage.HTML += `<img src="tg://photo?id=rich_photo_0"/>`
		}, want: "referenced more than once"},
		{name: "duplicate media id", mutate: func(req *SendRichMessageRequest) {
			req.RichMessage.Media[1].ID = req.RichMessage.Media[0].ID
		}, want: "media id"},
		{name: "duplicate attachment id", mutate: func(req *SendRichMessageRequest) {
			req.Attachments[1].ID = req.Attachments[0].ID
		}, want: "attachment id"},
		{name: "dangling upload", mutate: func(req *SendRichMessageRequest) {
			req.Attachments = req.Attachments[:1]
		}, want: "has no uploaded file"},
		{name: "unsafe attachment content type", mutate: func(req *SendRichMessageRequest) {
			req.Attachments[0].ContentType = "image/png\r\nx-test: injected"
		}, want: "invalid content type"},
		{name: "non image attachment content type", mutate: func(req *SendRichMessageRequest) {
			req.Attachments[0].ContentType = "text/plain"
		}, want: "not an image"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			client := &Client{
				httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					calls++
					return nil, errors.New("must not be called")
				})},
				apiURL: "https://api.telegram.invalid/botfake-token",
			}
			req := valid()
			tt.mutate(&req)

			message, err := client.SendRichMessage(context.Background(), req)

			require.Error(t, err)
			assert.Nil(t, message)
			assert.Contains(t, err.Error(), tt.want)
			assert.Zero(t, calls)
		})
	}
}

func TestSendMediaGroup_ReturnsEveryConfirmedMessageID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		require.NoError(t, json.NewEncoder(w).Encode(APIResponse{
			Ok: true,
			Result: json.RawMessage(`[
				{"message_id":101,"chat":{"id":123,"type":"private"}},
				{"message_id":102,"chat":{"id":123,"type":"private"}},
				{"message_id":103,"chat":{"id":123,"type":"private"}},
				{"message_id":104,"chat":{"id":123,"type":"private"}}
			]`),
		}))
	}))
	defer server.Close()

	client := &Client{token: "fake-token", httpClient: server.Client(), apiURL: server.URL + "/botfake-token"}
	messages, err := client.SendMediaGroup(context.Background(), SendMediaGroupRequest{
		ChatID: 123,
		Media: []InputMediaPhoto{
			{Data: []byte("one")}, {Data: []byte("two")},
			{Data: []byte("three")}, {Data: []byte("four")},
		},
	})

	require.NoError(t, err)
	require.Len(t, messages, 4)
	assert.Equal(t, []int{101, 102, 103, 104}, []int{
		messages[0].MessageID, messages[1].MessageID, messages[2].MessageID, messages[3].MessageID,
	})
}

func TestSendMediaGroup_RejectsMalformedConfirmation(t *testing.T) {
	tests := []struct {
		name   string
		result string
	}{
		{name: "missing result", result: `{"ok":true}`},
		{name: "null result", result: `{"ok":true,"result":null}`},
		{name: "wrong result type", result: `{"ok":true,"result":"messages"}`},
		{name: "empty result", result: `{"ok":true,"result":[]}`},
		{name: "truncated result", result: `{"ok":true,"result":[{"message_id":1}]}`},
		{name: "zero id", result: `{"ok":true,"result":[{"message_id":1},{"message_id":0}]}`},
		{name: "duplicate id", result: `{"ok":true,"result":[{"message_id":1},{"message_id":1}]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				_, _ = io.WriteString(w, tt.result)
			}))
			defer server.Close()

			client := &Client{token: "fake-token", httpClient: server.Client(), apiURL: server.URL + "/botfake-token"}
			messages, err := client.SendMediaGroup(context.Background(), SendMediaGroupRequest{
				ChatID: 123,
				Media:  []InputMediaPhoto{{Data: []byte("one")}, {Data: []byte("two")}},
			})

			require.Error(t, err)
			assert.Nil(t, messages)
			assert.Equal(t, 1, calls)
			var apiErr *APIError
			assert.False(t, errors.As(err, &apiErr), "malformed success has unknown outcome")
		})
	}
}

func makeRichPhotoUploads(count int) []RichPhotoUpload {
	uploads := make([]RichPhotoUpload, count)
	for i := range uploads {
		uploads[i] = RichPhotoUpload{
			Filename: fmt.Sprintf("generated_%d.png", i),
			MIME:     "image/png",
			Data:     []byte(fmt.Sprintf("png-%d", i)),
		}
	}
	return uploads
}

func richPhotoTestHTML(media []InputRichMessageMedia) string {
	var html strings.Builder
	if len(media) > 1 {
		html.WriteString("<tg-collage>")
	}
	for _, item := range media {
		fmt.Fprintf(&html, `<img src="tg://photo?id=%s"/>`, item.ID)
	}
	if len(media) > 1 {
		html.WriteString("</tg-collage>")
	}
	return html.String()
}
