// Package api содержит тесты и вспомогательные функции для клиента Yougile API.
package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"yougile_bot4/internal/metrics"
	"yougile_bot4/internal/models"
)

func TestCreateTaskParsesID(t *testing.T) {
	// mock server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/api-v2/board/1/tasks" {
			w.WriteHeader(http.StatusCreated)
			resp := map[string]interface{}{"data": map[string]interface{}{"id": 777}}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	m := metrics.NewMetrics()
	c := NewClient("token", "1", 0, m)
	c.baseURL = ts.URL
	c.httpClient = ts.Client()

	task := &models.Task{Title: "t"}
	if err := c.CreateTask(task); err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}

	if task.ID != 777 {
		t.Fatalf("expected task.ID 777, got %d", task.ID)
	}
}
