package storage

import (
	"encoding/json"
	"os"
	"yougile_bot4/internal/models"
)

// LoadTaskTemplates загружает шаблоны задач из файла
func (s *Storage) LoadTaskTemplates() error {
	data, err := os.ReadFile("data/task_templates.json")
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
	template, exists := s.taskTemplates[step]
	return template, exists
}
