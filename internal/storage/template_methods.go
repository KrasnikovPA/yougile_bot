package storage

import (
	"encoding/json"
	"os"
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

	return os.WriteFile(s.templatesFile, data, 0644)
}
