package bot

import (
	"fmt"
	"strings"
	"time"

	"yougile_bot4/internal/models"

	"gopkg.in/telebot.v3"
)

// handleTaskConstructor обрабатывает начало создания задачи через конструктор
func (b *Bot) handleTaskConstructor(c telebot.Context) error {
	user, exists := b.storage.GetUser(c.Sender().ID)
	if !exists || !user.Approved {
		return c.Send("Пожалуйста, сначала зарегистрируйтесь и дождитесь подтверждения администратора.")
	}

	// Инициализируем состояние создания задачи
	b.taskCreationStates[c.Sender().ID] = &models.TaskCreationState{
		StartTime:   time.Now(),
		CurrentStep: "initial",
		Stage:      "waiting_title", // для обратной совместимости
		Answers:     make(map[string]string),
		IsTemplated: true,
	}

	// Получаем первый шаг
	step, exists := b.storage.GetTaskTemplate("initial")
	if !exists {
		return c.Send("Извините, произошла ошибка при загрузке конструктора задач.")
	}

	// Создаем клавиатуру с вариантами
	menu := &telebot.ReplyMarkup{}
	var rows []telebot.Row
	for _, option := range step.Options {
		btn := menu.Data(option.Text, fmt.Sprintf("task_step|%s|%s", option.ID, option.Next))
		rows = append(rows, menu.Row(btn))
	}
	menu.Inline(rows...)

	return c.Send(step.Question, menu)
}

// handleTaskStepCallback обрабатывает выбор варианта в конструкторе задач
func (b *Bot) handleTaskStepCallback(c telebot.Context) error {
	state, exists := b.taskCreationStates[c.Sender().ID]
	if !exists || !state.IsTemplated {
		return c.Send("Сессия создания задачи истекла. Пожалуйста, начните заново.")
	}

	// Разбираем данные callback
	parts := strings.Split(c.Callback().Data, "|")
	if len(parts) != 3 {
		return c.Send("Ошибка обработки выбора. Пожалуйста, попробуйте еще раз.")
	}

	optionID := parts[1]
	nextStep := parts[2]

	// Сохраняем ответ
	state.Answers[state.CurrentStep] = optionID
	
	// Если следующий шаг "manual_input", переходим к ручному вводу
	if nextStep == "manual_input" {
		state.CurrentStep = "manual_input"
		return c.Send("Опишите задачу подробно:")
	}

	// Получаем следующий шаг
	step, exists := b.storage.GetTaskTemplate(nextStep)
	if !exists {
		return c.Send("Извините, произошла ошибка в конструкторе задач.")
	}

	state.CurrentStep = nextStep

	// Для input просто отправляем вопрос
	if step.Type == "input" {
		return c.Send(step.Question)
	}

	// Для multiselect создаем клавиатуру с чекбоксами
	if step.Type == "multiselect" {
		menu := &telebot.ReplyMarkup{}
		var rows []telebot.Row
		for _, option := range step.Options {
			selected := contains(state.Selections, option.ID)
			text := option.Text
			if selected {
				text = "✅ " + text
			}
			btn := menu.Data(text, fmt.Sprintf("task_select|%s", option.ID))
			rows = append(rows, menu.Row(btn))
		}
		rows = append(rows, menu.Row(
			menu.Data("✅ Подтвердить", "task_step|confirm|"+step.Next),
		))
		menu.Inline(rows...)
		return c.Send(step.Question, menu)
	}

	// Для обычного выбора создаем клавиатуру с вариантами
	menu := &telebot.ReplyMarkup{}
	var rows []telebot.Row
	for _, option := range step.Options {
		btn := menu.Data(option.Text, fmt.Sprintf("task_step|%s|%s", option.ID, option.Next))
		rows = append(rows, menu.Row(btn))
	}
	menu.Inline(rows...)

	return c.Send(step.Question, menu)
}

// handleTaskSelectCallback обрабатывает выбор опций в multiselect
func (b *Bot) handleTaskSelectCallback(c telebot.Context) error {
	state, exists := b.taskCreationStates[c.Sender().ID]
	if !exists || !state.IsTemplated {
		return c.Send("Сессия создания задачи истекла. Пожалуйста, начните заново.")
	}

	// Получаем ID опции
	parts := strings.Split(c.Callback().Data, "|")
	if len(parts) != 2 {
		return c.Send("Ошибка обработки выбора. Пожалуйста, попробуйте еще раз.")
	}
	optionID := parts[1]

	// Переключаем состояние опции
	if contains(state.Selections, optionID) {
		state.Selections = remove(state.Selections, optionID)
	} else {
		state.Selections = append(state.Selections, optionID)
	}

	// Обновляем сообщение с актуальным состоянием чекбоксов
	step, exists := b.storage.GetTaskTemplate(state.CurrentStep)
	if !exists {
		return c.Send("Извините, произошла ошибка в конструкторе задач.")
	}

	menu := &telebot.ReplyMarkup{}
	var rows []telebot.Row
	for _, option := range step.Options {
		selected := contains(state.Selections, option.ID)
		text := option.Text
		if selected {
			text = "✅ " + text
		}
		btn := menu.Data(text, fmt.Sprintf("task_select|%s", option.ID))
		rows = append(rows, menu.Row(btn))
	}
	rows = append(rows, menu.Row(
		menu.Data("✅ Подтвердить", "task_step|confirm|"+step.Next),
	))
	menu.Inline(rows...)

	// Используем EditReplyMarkup для обновления только клавиатуры
	return c.Edit(menu)
}

// Вспомогательные функции
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func remove(slice []string, item string) []string {
	result := make([]string, 0, len(slice))
	for _, s := range slice {
		if s != item {
			result = append(result, s)
		}
	}
	return result
}
