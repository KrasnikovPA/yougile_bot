package models

import "time"

// TaskTemplateOption представляет вариант ответа в конструкторе задач
type TaskTemplateOption struct {
	ID       string `json:"id"`       // Идентификатор опции
	Text     string `json:"text"`     // Текст для отображения
	Next     string `json:"next"`     // Следующий шаг
	Template string `json:"template"` // Шаблон для формирования текста задачи
}

// TaskTemplateStep представляет шаг в конструкторе задач
type TaskTemplateStep struct {
	Question string               `json:"question"` // Вопрос для пользователя
	Type     string               `json:"type"`     // Тип шага: select, input, multiselect
	Options  []TaskTemplateOption `json:"options"`  // Варианты ответов для select и multiselect
	Next     string               `json:"next"`     // Следующий шаг для input
	Template string               `json:"template"` // Шаблон для формирования текста
}

// TaskTemplates содержит все шаблоны задач
type TaskTemplates map[string]TaskTemplateStep

// TaskCreationState представляет состояние создания задачи
type TaskCreationState struct {
	StartTime   time.Time         // Время начала создания
	Stage       string            // Текущий этап (для обратной совместимости)
	CurrentStep string            // Текущий шаг в конструкторе
	Answers     map[string]string // Ответы пользователя
	Selections  []string          // Выбранные опции для multiselect
	Title       string            // Название задачи
	Description string            // Описание задачи
	IsTemplated bool              // Используется ли конструктор
}
