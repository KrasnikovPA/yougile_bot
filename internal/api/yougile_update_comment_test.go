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

// TestUpdateTaskRetries проверяет, что UpdateTask делает retry при 500 и успешно завершается при 200
func TestUpdateTaskRetries(t *testing.T) {
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}
		if r.Method != "PUT" {
			t.Fatalf("expected PUT, got %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"data": {}}`)
	}))
	defer ts.Close()

	c := NewClient("token", "board", 2*time.Second, &metrics.Metrics{})
	c.baseURL = ts.URL
	c.httpClient = ts.Client()
	c.retryCount = 3
	c.retryWait = 10 * time.Millisecond

	task := &models.Task{ID: 42, Title: "update me"}
	if err := c.UpdateTask(task); err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if calls < 2 {
		t.Fatalf("expected at least 2 calls, got %d", calls)
	}
}

// TestAddCommentRetries проверяет, что AddComment делает retry при 500 и успешно завершается при 201
func TestAddCommentRetries(t *testing.T) {
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
		io.WriteString(w, `{"data": {"id": 999}}`)
	}))
	defer ts.Close()

	c := NewClient("token", "board", 2*time.Second, &metrics.Metrics{})
	c.baseURL = ts.URL
	c.httpClient = ts.Client()
	c.retryCount = 3
	c.retryWait = 10 * time.Millisecond

	comment := &models.Comment{Text: "hello"}
	if err := c.AddComment(123, comment); err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if calls < 2 {
		t.Fatalf("expected at least 2 calls, got %d", calls)
	}
}
