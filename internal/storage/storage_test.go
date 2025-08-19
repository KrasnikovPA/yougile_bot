// Package storage содержит тесты для файлового хранилища приложения.
package storage

import (
	"os"
	"testing"
	"time"

	"yougile_bot4/internal/metrics"
	"yougile_bot4/internal/models"
)

func TestStorageSaveLoadTasks(t *testing.T) {
	dir := "data/test_storage"
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("Ошибка очистки тестовой директории: %v", err)
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("Ошибка создания тестовой директории: %v", err)
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Logf("Ошибка удаления тестовой директории при завершении: %v", err)
		}
	}()

	known := dir + "/known.json"
	chats := dir + "/chats.json"
	users := dir + "/users.json"
	tasks := dir + "/tasks.json"
	templates := dir + "/templates.json"

	m := metrics.NewMetrics()
	s, err := NewStorage(known, chats, users, tasks, templates, m)
	if err != nil {
		t.Fatalf("NewStorage failed: %v", err)
	}

	t1 := &models.Task{ID: 12345, Title: "t1", CreatedAt: time.Now()}
	s.AddTask(t1)

	// Run SaveData with timeout to detect hangs
	t.Logf("Calling SaveData...")
	done := make(chan error, 1)
	go func() {
		done <- s.SaveData()
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SaveData failed: %v", err)
		}
		t.Logf("SaveData completed")
	case <-time.After(5 * time.Second):
		t.Fatalf("SaveData timed out (possible deadlock)")
	}

	// create new storage to load
	s2, err := NewStorage(known, chats, users, tasks, templates, m)
	if err != nil {
		t.Fatalf("NewStorage load failed: %v", err)
	}

	tasksLoaded := s2.GetTasks()
	if len(tasksLoaded) != 1 || tasksLoaded[0].ID != 12345 {
		t.Fatalf("tasks not persisted, got: %+v", tasksLoaded)
	}
}
