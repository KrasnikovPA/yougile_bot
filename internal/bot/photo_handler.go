// Package bot обрабатывает получаемые от пользователей фотографии и вложения.
package bot

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"yougile_bot4/internal/models"

	"gopkg.in/telebot.v3"
)

// handlePhoto обрабатывает сообщения с фотографиями
func (b *Bot) handlePhoto(c telebot.Context) error {
	// Проверяем, что пользователь авторизован
	user, exists := b.storage.GetUser(c.Sender().ID)
	if !exists || !user.Approved {
		return c.Send("Пожалуйста, сначала зарегистрируйтесь и дождитесь подтверждения администратора.")
	}

	// Проверяем состояние создания задачи
	if state, ok := b.taskCreationStates[c.Sender().ID]; ok && state.Stage == "waiting_comment" {
		// Получаем фото из сообщения
		photo := c.Message().Photo
		if photo == nil {
			return c.Send("Ошибка при получении фотографии.")
		}

		// Создаем временный файл для фото
		tmpFile, err := os.CreateTemp("", "telegram_photo_*.jpg")
		if err != nil {
			log.Printf("Ошибка создания временного файла: %v", err)
			return c.Send("Ошибка при обработке фотографии.")
		}
		tmpName := tmpFile.Name()
		// Close the file handle so other processes (telebot.Download) can write/replace it and we can remove it later
		if cerr := tmpFile.Close(); cerr != nil {
			log.Printf("Ошибка закрытия временного файла %s: %v", tmpName, cerr)
		}
		defer func() {
			if err := os.Remove(tmpName); err != nil {
				log.Printf("Ошибка удаления временного файла %s: %v", tmpName, err)
			}
		}()

		// Скачиваем файл
		err = b.bot.Download(&photo.File, tmpFile.Name())
		if err != nil {
			log.Printf("Ошибка загрузки файла: %v", err)
			return c.Send("Ошибка при загрузке фотографии.")
		}

		// Читаем файл в память
		fileData, err := os.ReadFile(tmpFile.Name())
		if err != nil {
			log.Printf("Ошибка чтения файла: %v", err)
			return c.Send("Ошибка при обработке фотографии.")
		}

		caption := c.Message().Caption
		if caption == "" {
			caption = "[Фотография к задаче]"
		}

		// Создаем задачу с фотографией
		user, _ := b.storage.GetUser(c.Sender().ID)
		desc := caption
		if user != nil {
			desc = b.formatTaskDescription(user, caption)
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

		if task.ColumnID == "" {
			task.ColumnID = b.defaultColumn
		}

		// Отправляем задачу в Yougile
		if err := b.yougileClient.CreateTask(task); err != nil {
			log.Printf("Ошибка создания задачи в Yougile: %v", err)
			return c.Send("Произошла ошибка при создании задачи. Пожалуйста, попробуйте позже.")
		}

		// Загружаем фотографию в Yougile (POST /api-v2/upload-file) и публикуем её в чате
		// задачи вместе с подписью — это единственный способ прикрепить файл к задаче
		// в реальном API Yougile (см. UploadFile/AddComment в internal/api/yougile.go).
		attID := fmt.Sprintf("img_%d.jpg", time.Now().Unix())

		taskIDStr := task.ExternalID
		if taskIDStr == "" {
			taskIDStr = strconv.FormatInt(task.ID, 10)
		}

		if fullURL, uerr := b.yougileClient.UploadFile(attID, fileData); uerr != nil {
			log.Printf("Ошибка загрузки фотографии в Yougile: %v", uerr)
		} else {
			comment := &models.Comment{
				TaskID:      task.ID,
				AuthorID:    strconv.FormatInt(c.Sender().ID, 10),
				Text:        caption,
				Attachments: []models.Attachment{{ID: attID, Type: models.AttachmentTypeImage, URL: fullURL, CreatedAt: time.Now()}},
				CreatedAt:   time.Now(),
				UpdatedAt:   time.Now(),
			}
			if cerr := b.yougileClient.AddComment(taskIDStr, comment); cerr != nil {
				log.Printf("Ошибка добавления фотографии к задаче: %v", cerr)
			} else {
				task.Comments = append(task.Comments, *comment)
			}
		}

		// Сохраняем задачу локально
		b.storage.AddTask(task)
		if err := b.storage.SaveData(); err != nil {
			log.Printf("Ошибка сохранения задачи: %v", err)
		}

		// Запускаем проверку создания задачи
		b.startTaskVerification(*task, *user, caption, true, fileData)

		delete(b.taskCreationStates, c.Sender().ID)
		if err := c.Send("Задача с фотографией отправлена на создание. Вы получите уведомление после её успешного создания.", b.menuForContext(c)); err != nil {
			log.Printf("Ошибка отправки пользователю подтверждения отправки задачи: %v", err)
		}
		return nil
	}

	// Проверяем, находится ли пользователь в процессе комментирования задачи
	if taskID, ok := b.commentStates[c.Sender().ID]; ok {
		// Получаем фото из сообщения
		photo := c.Message().Photo
		if photo == nil {
			return c.Send("Ошибка при получении фотографии.")
		}

		// Создаем временный файл для фото
		tmpFile, err := os.CreateTemp("", "telegram_photo_*.jpg")
		if err != nil {
			log.Printf("Ошибка создания временного файла: %v", err)
			return c.Send("Ошибка при обработке фотографии.")
		}
		tmpName := tmpFile.Name()
		if cerr := tmpFile.Close(); cerr != nil {
			log.Printf("Ошибка закрытия временного файла %s: %v", tmpName, cerr)
		}
		defer func() {
			if err := os.Remove(tmpName); err != nil {
				log.Printf("Ошибка удаления временного файла %s: %v", tmpName, err)
			}
		}()

		// Скачиваем файл
		err = b.bot.Download(&photo.File, tmpFile.Name())
		if err != nil {
			log.Printf("Ошибка загрузки файла: %v", err)
			return c.Send("Ошибка при загрузке фотографии.")
		}

		// Читаем файл в память
		fileData, err := os.ReadFile(tmpFile.Name())
		if err != nil {
			log.Printf("Ошибка чтения файла: %v", err)
			return c.Send("Ошибка при обработке фотографии.")
		}

		attID := fmt.Sprintf("img_%d.jpg", time.Now().Unix())

		caption := c.Message().Caption
		if caption == "" {
			caption = "[Фотография]"
		}

		// Загружаем фотографию в Yougile и публикуем её в чате задачи вместе с подписью.
		fullURL, uerr := b.yougileClient.UploadFile(attID, fileData)
		if uerr != nil {
			log.Printf("Ошибка загрузки фотографии в Yougile: %v", uerr)
			if err2 := c.Send("Ошибка при загрузке фотографии в Yougile."); err2 != nil {
				log.Printf("Ошибка отправки сообщения об ошибке пользователю: %v", err2)
			}
			return nil
		}

		comment := &models.Comment{
			TaskID:      taskID,
			AuthorID:    strconv.FormatInt(c.Sender().ID, 10),
			Text:        caption,
			Attachments: []models.Attachment{{ID: attID, Type: models.AttachmentTypeImage, URL: fullURL, CreatedAt: time.Now()}},
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		}

		// Добавляем комментарий к задаче: предпочитаем ExternalID из хранилища как id задачи в Yougile
		taskIDStr := strconv.FormatInt(taskID, 10)
		tasks := b.storage.GetTasks()
		for _, t := range tasks {
			if t.ID == taskID && t.ExternalID != "" {
				taskIDStr = t.ExternalID
				break
			}
		}
		if err := b.yougileClient.AddComment(taskIDStr, comment); err != nil {
			log.Printf("Ошибка добавления комментария: %v", err)
			if err2 := c.Send("Ошибка при добавлении комментария с фотографией."); err2 != nil {
				log.Printf("Ошибка отправки сообщения об ошибке пользователю: %v", err2)
			}
			return nil
		}

		// Persist comment locally
		for _, t := range tasks {
			if t.ID == taskID {
				t.Comments = append(t.Comments, *comment)
				b.storage.UpdateTask(t)
				if sErr := b.storage.SaveData(); sErr != nil {
					log.Printf("Ошибка сохранения комментария в хранилище: %v", sErr)
				}
				break
			}
		}

		delete(b.commentStates, c.Sender().ID)
		if err := c.Send("Фотография успешно добавлена к задаче в Yougile.", b.menuForContext(c)); err != nil {
			log.Printf("Ошибка отправки подтверждения пользователю: %v", err)
		}
		return nil
	}

	if err := c.Send("Пожалуйста, сначала начните создание новой задачи или выберите задачу для комментирования.", b.menuForContext(c)); err != nil {
		log.Printf("Ошибка отправки подсказки пользователю: %v", err)
	}
	return nil
}
