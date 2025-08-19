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

// Тест проверяет, что UploadAttachment делает retry при 500 и успешно завершается при 201
func TestUploadAttachmentRetries(t *testing.T) {
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		if _, err := io.WriteString(w, `{"data": {"id": 123}}`); err != nil {
			t.Fatalf("Ошибка записи тела ответа в тесте: %v", err)
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

	if err := c.UploadAttachment(1, attachment, data); err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}

	if calls < 2 {
		t.Fatalf("expected at least 2 calls, got %d", calls)
	}
}
