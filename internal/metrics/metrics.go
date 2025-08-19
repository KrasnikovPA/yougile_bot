// Package metrics содержит счётчики и метрики, используемые в приложении.
package metrics

import (
	"sync"
	"sync/atomic"
	"time"
)

// Metrics хранит метрики работы приложения
type Metrics struct {
	ActiveUsers    int64
	TasksCreated   int64
	AdminActions   int64
	APIRequests    int64
	APIErrors      int64
	AverageLatency time.Duration
	mu             sync.RWMutex
}

// NewMetrics создает новый экземпляр метрик
func NewMetrics() *Metrics {
	return &Metrics{}
}

// IncActiveUsers увеличивает счетчик активных пользователей
func (m *Metrics) IncActiveUsers() { atomic.AddInt64(&m.ActiveUsers, 1) }

// DecActiveUsers уменьшает счетчик активных пользователей
func (m *Metrics) DecActiveUsers() { atomic.AddInt64(&m.ActiveUsers, -1) }

// IncTasksCreated увеличивает счетчик созданных задач
func (m *Metrics) IncTasksCreated() { atomic.AddInt64(&m.TasksCreated, 1) }

// IncAdminActions увеличивает счетчик административных действий
func (m *Metrics) IncAdminActions() { atomic.AddInt64(&m.AdminActions, 1) }

// IncAPIRequests увеличивает счетчик API запросов
func (m *Metrics) IncAPIRequests() { atomic.AddInt64(&m.APIRequests, 1) }

// IncAPIErrors увеличивает счетчик ошибок API
func (m *Metrics) IncAPIErrors() { atomic.AddInt64(&m.APIErrors, 1) }

// UpdateLatency обновляет среднее время ответа
func (m *Metrics) UpdateLatency(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Простое скользящее среднее
	if m.AverageLatency == 0 {
		m.AverageLatency = d
	} else {
		m.AverageLatency = (m.AverageLatency + d) / 2
	}
}

// GetStats возвращает текущие метрики
func (m *Metrics) GetStats() map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return map[string]interface{}{
		"active_users":    atomic.LoadInt64(&m.ActiveUsers),
		"tasks_created":   atomic.LoadInt64(&m.TasksCreated),
		"admin_actions":   atomic.LoadInt64(&m.AdminActions),
		"api_requests":    atomic.LoadInt64(&m.APIRequests),
		"api_errors":      atomic.LoadInt64(&m.APIErrors),
		"average_latency": m.AverageLatency.String(),
	}
}
