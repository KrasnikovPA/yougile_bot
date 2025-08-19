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

// Тест проверяет, что CreateTask делает retry при 500 и парсит ID при 201
func TestCreateTaskRetriesAndParseID(t *testing.T) {
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}
		if r.Method != "POST" {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"data": {"id": 777}}`)
	}))
	defer ts.Close()

	c := NewClient("token", "board", 2*time.Second, &metrics.Metrics{})
	c.baseURL = ts.URL
	c.httpClient = ts.Client()
	c.retryCount = 3
	c.retryWait = 10 * time.Millisecond

	task := &models.Task{Title: "hello"}

	if err := c.CreateTask(task); err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}

	if task.ID != 777 {
		t.Fatalf("expected task ID to be 777, got %d", task.ID)
	}

	if calls < 2 {
		t.Fatalf("expected at least 2 calls, got %d", calls)
	}
}
