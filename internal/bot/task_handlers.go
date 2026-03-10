// Package bot содержит обработчики задач и проверки валидации.
package bot

import (
	"fmt"
	"log"
	"strconv"
	"time"
	"yougile_bot4/internal/models"

	"gopkg.in/telebot.v3"
)

// handleNewTask ранее использовалась для простого создания задачи.
// Сейчас заменена на более гибкий конструктор задач: handleTaskConstructor.
// Функция удалена как неиспользуемая, чтобы убрать предупреждение компилятора/IDE.

// handleSkip обрабатывает нажатие кнопки "Без комментария"
func (b *Bot) handleSkip(c telebot.Context) error {
	state, exists := b.taskCreationStates[c.Sender().ID]
	if !exists || state.Stage != "waiting_comment" {
		return nil
	}

	// Создаем задачу без комментария
	user, _ := b.storage.GetUser(c.Sender().ID)
	desc := ""
	if user != nil {
		desc = b.formatTaskDescription(user, "")
	}
	task := &models.Task{
		Title:       b.formatTaskTitle(user, state.Title),
		Description: desc,
		Status:      models.TaskStatusNew,
		BoardID:     b.boardID,
		Priority:    1,
		Assignee:    strconv.FormatInt(c.Sender().ID, 10),
		Labels:      []string{},
		CreatedAt:   time.Now(),
	}

	// set default column if configured
	if task.ColumnID == "" {
		task.ColumnID = b.defaultColumn
	}
	// Отправляем задачу в Yougile
	if err := b.yougileClient.CreateTask(task); err != nil {
		log.Printf("Ошибка создания задачи в Yougile: %v", err)
		return c.Send("Произошла ошибка при создании задачи. Пожалуйста, попробуйте позже.")
	}

	// Сохраняем задачу локально
	b.storage.AddTask(task)
	if err := b.storage.SaveData(); err != nil {
		log.Printf("Ошибка сохранения задачи: %v", err)
	}

	// Запускаем проверку создания задачи
	if user != nil {
		b.startTaskVerification(*task, *user, "", false, nil)
	} else {
		dummy := &models.User{TelegramID: c.Sender().ID}
		b.startTaskVerification(*task, *dummy, "", false, nil)
	}

	// Удаляем состояние создания задачи
	delete(b.taskCreationStates, c.Sender().ID)
	return c.Send("Задача отправлена на создание. Вы получите уведомление после её успешного создания.", b.menuForContext(c))
}

// handleTaskText обрабатывает текстовые сообщения при создании задачи
func (b *Bot) handleTaskText(c telebot.Context) error {
	state, exists := b.taskCreationStates[c.Sender().ID]
	if !exists {
		return nil
	}

	msg := c.Text()
	switch state.Stage {
	case "waiting_title":
		if len(msg) < 3 {
			return c.Send("Название задачи должно содержать минимум 3 символа. Пожалуйста, попробуйте снова.")
		}
		state.Title = msg
		state.Stage = "waiting_comment"
		return c.Send("Отлично! Теперь вы можете добавить комментарий или фотографию к задаче.", commentMenu)

	case "waiting_comment":
		if len(msg) < b.minMsgLen {
			return c.Send(fmt.Sprintf("Комментарий слишком короткий. Минимальная длина: %d символов.", b.minMsgLen))
		}

		// Создаем новую задачу с комментарием
		user, _ := b.storage.GetUser(c.Sender().ID)
		desc := msg
		if user != nil {
			desc = b.formatTaskDescription(user, msg)
		}
		task := &models.Task{
			Title:       b.formatTaskTitle(user, state.Title),
			Description: desc,
			Status:      models.TaskStatusNew,
			Priority:    1,
			Assignee:    strconv.FormatInt(c.Sender().ID, 10),
			BoardID:     b.boardID,
			Labels:      []string{},
			CreatedAt:   time.Now(),
		}

		if task.ColumnID == "" {
			task.ColumnID = b.defaultColumn
		}
		// Отправляем задачу в Yougile
		if err := b.yougileClient.CreateTask(task); err != nil {
			log.Printf("Ошибка создания задачи в Yougile: %v", err)
			return c.Send("Произошла ошибка при создании задачи. Пожалуйста, попробуйте позже.")
		}

		// Сохраняем задачу локально
		b.storage.AddTask(task)
		if err := b.storage.SaveData(); err != nil {
			log.Printf("Ошибка сохранения задачи: %v", err)
		}

		// Запускаем проверку создания задачи
		if user != nil {
			b.startTaskVerification(*task, *user, msg, false, nil)
		} else {
			dummy := &models.User{TelegramID: c.Sender().ID}
			b.startTaskVerification(*task, *dummy, msg, false, nil)
		}

		delete(b.taskCreationStates, c.Sender().ID)
		return c.Send("Задача отправлена на создание. Вы получите уведомление после её успешного создания.", b.menuForContext(c))
	}

	return nil
}
