// Package metrics содержит тесты для счётчиков метрик приложения.
package metrics

import (
	"testing"
	"time"
)

func TestCounters(t *testing.T) {
	m := NewMetrics()

	m.IncActiveUsers()
	m.IncActiveUsers()
	m.DecActiveUsers()
	m.IncTasksCreated()
	m.IncAdminActions()
	m.IncAPIRequests()
	m.IncAPIRequests()
	m.IncAPIErrors()

	if m.ActiveUsers != 1 {
		t.Errorf("expected ActiveUsers=1, got %d", m.ActiveUsers)
	}
	if m.TasksCreated != 1 {
		t.Errorf("expected TasksCreated=1, got %d", m.TasksCreated)
	}
	if m.AdminActions != 1 {
		t.Errorf("expected AdminActions=1, got %d", m.AdminActions)
	}
	if m.APIRequests != 2 {
		t.Errorf("expected APIRequests=2, got %d", m.APIRequests)
	}
	if m.APIErrors != 1 {
		t.Errorf("expected APIErrors=1, got %d", m.APIErrors)
	}
}

func TestUpdateLatencyMovingAverage(t *testing.T) {
	m := NewMetrics()

	// Первый вызов задаёт значение напрямую.
	m.UpdateLatency(100 * time.Millisecond)
	if m.AverageLatency != 100*time.Millisecond {
		t.Fatalf("expected AverageLatency=100ms after first update, got %v", m.AverageLatency)
	}

	// Второй вызов усредняет с предыдущим значением: (100ms + 200ms) / 2 = 150ms.
	m.UpdateLatency(200 * time.Millisecond)
	if m.AverageLatency != 150*time.Millisecond {
		t.Fatalf("expected AverageLatency=150ms after second update, got %v", m.AverageLatency)
	}
}

func TestGetStats(t *testing.T) {
	m := NewMetrics()
	m.IncActiveUsers()
	m.IncTasksCreated()
	m.IncTasksCreated()
	m.IncAdminActions()
	m.IncAPIRequests()
	m.IncAPIErrors()
	m.UpdateLatency(50 * time.Millisecond)

	stats := m.GetStats()

	want := map[string]interface{}{
		"active_users":    int64(1),
		"tasks_created":   int64(2),
		"admin_actions":   int64(1),
		"api_requests":    int64(1),
		"api_errors":      int64(1),
		"average_latency": (50 * time.Millisecond).String(),
	}

	for key, expected := range want {
		got, ok := stats[key]
		if !ok {
			t.Errorf("GetStats() missing key %q", key)
			continue
		}
		if got != expected {
			t.Errorf("GetStats()[%q] = %v, want %v", key, got, expected)
		}
	}
}
