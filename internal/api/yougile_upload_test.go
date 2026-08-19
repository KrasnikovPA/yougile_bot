// Package api содержит тесты и вспомогательные функции для клиента Yougile API.
package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"yougile_bot4/internal/metrics"
	"yougile_bot4/internal/models"
)

// Тест проверяет, что UploadAttachment (upload-file + сообщение в чате задачи) делает retry
// при 500 на каждом из двух реальных запросов и успешно завершается при успешном ответе.
func TestUploadAttachmentRetries(t *testing.T) {
	uploadCalls, chatCalls := 0, 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api-v2/upload-file":
			uploadCalls++
			if uploadCalls == 1 {
				http.Error(w, "server error", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
			if _, err := io.WriteString(w, `{"result":"ok","url":"/f.png","fullUrl":"https://example.com/f.png"}`); err != nil {
				t.Fatalf("Ошибка записи тела ответа в тесте: %v", err)
			}
		case r.URL.Path == "/api-v2/chats/1/messages":
			chatCalls++
			if chatCalls == 1 {
				http.Error(w, "server error", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusCreated)
			if _, err := io.WriteString(w, `{"id": 123}`); err != nil {
				t.Fatalf("Ошибка записи тела ответа в тесте: %v", err)
			}
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	c := NewClient("token", "board", 2*time.Second, &metrics.Metrics{})
	c.baseURL = ts.URL
	c.httpClient = ts.Client()
	c.retryCount = 3
	c.retryWait = 10 * time.Millisecond

	attachment := &models.Attachment{ID: "file1", Type: models.AttachmentTypeFile}
	data := []byte("hello")

	if err := c.UploadAttachment("1", attachment, data); err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if attachment.URL != "https://example.com/f.png" {
		t.Fatalf("expected attachment.URL to be set from upload-file response, got %q", attachment.URL)
	}

	if uploadCalls < 2 {
		t.Fatalf("expected at least 2 upload-file calls, got %d", uploadCalls)
	}
	if chatCalls < 2 {
		t.Fatalf("expected at least 2 chat message calls, got %d", chatCalls)
	}
}
