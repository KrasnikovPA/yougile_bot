// Package bot содержит обработчики и логику Telegram-бота.
package bot

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
	"yougile_bot4/internal/models"

	"gopkg.in/telebot.v3"
)

// TaskVerification описывает состояние проверки корректности созданной задачи
// (сравнение локально отправлённой задачи и результата в Yougile).
type TaskVerification struct {
	OriginalTask    models.Task
	OriginalSender  models.User
	OriginalContent string
	HasImage        bool
	ImageData       []byte
	RetryCount      int
	CreatedAt       time.Time
}

// startTaskVerification запускает процесс проверки задачи
func (b *Bot) startTaskVerification(task models.Task, sender models.User, content string, hasImage bool, imageData []byte) {
	verification := &TaskVerification{
		OriginalTask:    task,
		OriginalSender:  sender,
		OriginalContent: content,
		HasImage:        hasImage,
		ImageData:       imageData,
		RetryCount:      0,
		CreatedAt:       time.Now(),
	}

	// Запускаем проверку через 2 минуты
	time.AfterFunc(2*time.Minute, func() {
		b.verifyTask(verification)
	})
}

// verifyTask проверяет корректность создания задачи
func (b *Bot) verifyTask(v *TaskVerification) {
	// Получаем актуальную задачу из Yougile
	tasks, err := b.yougileClient.GetTasks(100) // Получаем последние задачи
	if err != nil {
		b.notifyError(v, "Ошибка при получении задач из Yougile")
		return
	}

	var foundTask *models.Task
	for i := range tasks {
		// Match by numeric ID
		if tasks[i].ID != 0 && v.OriginalTask.ID != 0 && tasks[i].ID == v.OriginalTask.ID {
			foundTask = &tasks[i]
			break
		}
		// Match by ExternalID (UUID or string id)
		if v.OriginalTask.ExternalID != "" && tasks[i].ExternalID != "" && tasks[i].ExternalID == v.OriginalTask.ExternalID {
			foundTask = &tasks[i]
			break
		}
		// Some servers may return numeric ID while original stored ExternalID as string; compare string forms
		if v.OriginalTask.ExternalID != "" {
			if fmt.Sprintf("%d", tasks[i].ID) == v.OriginalTask.ExternalID {
				foundTask = &tasks[i]
				break
			}
		}
	}

	if foundTask == nil {
		log.Printf("verifyTask: initial list scan did not find task. Original IDs: ID=%d ExternalID=%s Title=%s", v.OriginalTask.ID, v.OriginalTask.ExternalID, v.OriginalTask.Title)
		// Try direct lookup by ExternalID or string ID as a fallback
		if v.OriginalTask.ExternalID != "" {
			if t, err := b.yougileClient.GetTaskByID(v.OriginalTask.ExternalID); err == nil && t != nil {
				log.Printf("verifyTask: found task via GetTaskByID by ExternalID=%s -> ID=%d Title=%s", v.OriginalTask.ExternalID, t.ID, t.Title)
				foundTask = t
			} else if err != nil {
				log.Printf("verifyTask: GetTaskByID by ExternalID=%s error: %v", v.OriginalTask.ExternalID, err)
			}
		}

		// As a last resort, try to find by title or description substring in the list we already fetched
		if foundTask == nil {
			for i := range tasks {
				if tasks[i].Title == v.OriginalTask.Title || strings.Contains(tasks[i].Description, v.OriginalContent) {
					log.Printf("verifyTask: matched by title/description -> ID=%d ExternalID=%s Title=%s", tasks[i].ID, tasks[i].ExternalID, tasks[i].Title)
					foundTask = &tasks[i]
					break
				}
			}
		}

		if foundTask == nil {
			b.handleVerificationFailure(v, "Задача не найдена в Yougile")
			return
		}
	}

	// Проверяем корректность данных
	if !verifyTaskContent(foundTask, v) {
		b.handleVerificationFailure(v, "Несоответствие содержимого задачи")
		return
	}

	if v.HasImage {
		if !verifyTaskAttachments(foundTask) {
			b.handleVerificationFailure(v, "Отсутствует или некорректно загружено изображение")
			return
		}
	}

	// Если мы дошли до этого места — верификация успешна. Уведомим отправителя и админов.
	// Сформируем идентификатор задачи для отображения (ExternalID предпочтительнее)
	taskIDStr := ""
	if foundTask.ExternalID != "" {
		taskIDStr = foundTask.ExternalID
	} else if foundTask.ID != 0 {
		taskIDStr = strconv.FormatInt(foundTask.ID, 10)
	}

	successMsg := fmt.Sprintf("✅ Задача успешно создана в Yougile:\n📎 %s\n🆔 %s", foundTask.Title, taskIDStr)
	if _, err := b.bot.Send(&telebot.User{ID: v.OriginalSender.TelegramID}, successMsg); err != nil {
		log.Printf("verifyTask: ошибка отправки подтверждения отправителю %d: %v", v.OriginalSender.TelegramID, err)
	}

	// Уведомим администраторов краткой заметкой
	adminNote := fmt.Sprintf("Пользователь %s %s создал задачу: %s (ID: %s)", v.OriginalSender.FirstName, v.OriginalSender.LastName, foundTask.Title, taskIDStr)
	users := b.storage.GetUsers()
	for _, user := range users {
		if user.Role == models.RoleAdmin {
			// Don't notify the original sender twice if they are also an admin
			if user.TelegramID == v.OriginalSender.TelegramID {
				continue
			}
			if _, err := b.bot.Send(&telebot.User{ID: user.TelegramID}, adminNote); err != nil {
				log.Printf("verifyTask: ошибка отправки уведомления администратору %d: %v", user.TelegramID, err)
			}
		}
	}
}

// verifyTaskContent проверяет соответствие содержимого задачи
func verifyTaskContent(task *models.Task, v *TaskVerification) bool {
	// Проверяем название и описание задачи
	if task.Title != v.OriginalTask.Title {
		return false
	}

	// Если это повторная попытка, проверяем наличие пометки
	if v.RetryCount > 0 {
		expectedMark := "повторно исправлено"
		if !strings.Contains(task.Title, expectedMark) {
			return false
		}
	}

	// Проверяем описание, если оно есть
	if v.OriginalContent != "" && !strings.Contains(task.Description, v.OriginalContent) {
		return false
	}

	return true
}

// verifyTaskAttachments проверяет наличие вложений в задаче
func verifyTaskAttachments(task *models.Task) bool {
	return len(task.Attachments) > 0
}

// handleVerificationFailure обрабатывает неудачную проверку
func (b *Bot) handleVerificationFailure(v *TaskVerification, reason string) {
	if v.RetryCount == 0 {
		// Первая попытка не удалась, создаем новую задачу
		newTask := v.OriginalTask
		newTask.Title = fmt.Sprintf("%s (повторно исправлено)", newTask.Title)
		// ensure column is present for recreation
		if newTask.ColumnID == "" {
			newTask.ColumnID = b.defaultColumn
		}
		// we intentionally do not set Assigned here

		err := b.yougileClient.CreateTask(&newTask)
		if err != nil {
			b.notifyError(v, fmt.Sprintf("Ошибка при повторном создании задачи: %v", err))
			return
		}

		if v.HasImage {
			// Повторно прикрепляем изображение
			attachment := &models.Attachment{
				ID:   fmt.Sprintf("retry_%d", time.Now().Unix()),
				Type: models.AttachmentTypeImage,
			}
			// choose id string
			taskIDStr := newTask.ExternalID
			if taskIDStr == "" {
				taskIDStr = strconv.FormatInt(newTask.ID, 10)
			}
			err = b.yougileClient.UploadAttachment(taskIDStr, attachment, v.ImageData)
			if err != nil {
				b.notifyError(v, fmt.Sprintf("Ошибка при повторной загрузке изображения: %v", err))
				return
			}
		}

		// Запускаем повторную проверку
		v.RetryCount++
		v.OriginalTask = newTask
		time.AfterFunc(2*time.Minute, func() {
			b.verifyTask(v)
		})
	} else {
		// Вторая попытка также не удалась
		b.notifyError(v, reason)
	}
}

// notifyError уведомляет пользователя и администраторов об ошибке
func (b *Bot) notifyError(v *TaskVerification, reason string) {
	// Уведомляем отправителя
	errorMsg := fmt.Sprintf("Произошла проблема с созданием задачи: %s\nПожалуйста, обратитесь к администратору.", reason)
	if _, err := b.bot.Send(&telebot.User{ID: v.OriginalSender.TelegramID}, errorMsg); err != nil {
		// Логируем ошибку, но продолжаем уведомлять администраторов
		// чтобы они могли принять меры вручную.
		// Не возвращаем ошибку, потому что вызывающая горутина ожидает завершения.
		// Просто логируем проблему.
		// Используем стандартный log здесь — пакет bot не импортирует log ранее.
		// Добавим импорт логирования сверху файла.
		// (Импорт предварительно уже есть в файле; если нет — поправим отдельно.)
		// Но log импорт отсутствует, добавим его.
		// Для совместимости: используем fmt.Printf как fallback.
		// Поскольку в этом файле нет импорта log, выполню простое fmt.Printf.
		fmt.Printf("Ошибка отправки уведомления отправителю %d: %v\n", v.OriginalSender.TelegramID, err)
	}

	// Формируем сообщение для администраторов
	adminMsg := fmt.Sprintf(`❌ Ошибка создания задачи
Причина: %s

📤 Отправитель: %s %s
📝 Исходный текст: %s
📋 Текст в Yougile: %s

🔄 Количество попыток: %d
⏰ Время создания: %s`,
		reason,
		v.OriginalSender.FirstName,
		v.OriginalSender.LastName,
		v.OriginalContent,
		v.OriginalTask.Title,
		v.RetryCount+1,
		v.CreatedAt.Format("2006-01-02 15:04:05"))

	// Отправляем сообщение всем администраторам
	users := b.storage.GetUsers()
	for _, user := range users {
		if user.Role == models.RoleAdmin {
			if _, err := b.bot.Send(&telebot.User{ID: user.TelegramID}, adminMsg); err != nil {
				fmt.Printf("Ошибка отправки уведомления администратору %d: %v\n", user.TelegramID, err)
			}
		}
	}
}
