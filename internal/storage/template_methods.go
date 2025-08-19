// Package storage содержит методы для работы с шаблонами задач в хранилище.
package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"yougile_bot4/internal/models"
)

// LoadTaskTemplates загружает шаблоны задач из файла
func (s *Storage) LoadTaskTemplates() error {
	data, err := os.ReadFile(s.templatesFile)
	if err != nil {
		return err
	}

	var templates models.TaskTemplates
	if err := json.Unmarshal(data, &templates); err != nil {
		return err
	}

	s.taskTemplates = templates
	return nil
}

// GetTaskTemplate возвращает шаблон для определенного шага
func (s *Storage) GetTaskTemplate(step string) (models.TaskTemplateStep, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	template, exists := s.taskTemplates[step]
	return template, exists
}

// SaveTaskTemplates сохраняет шаблоны задач в файл
func (s *Storage) SaveTaskTemplates() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.MarshalIndent(s.taskTemplates, "", "    ")
	if err != nil {
		return err
	}

	dir := filepath.Dir(s.templatesFile)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	tmp := s.templatesFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, s.templatesFile)
}
