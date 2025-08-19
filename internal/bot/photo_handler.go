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
		task := &models.Task{
			Title:       state.Title,
			Description: caption,
			Status:      models.TaskStatusNew,
			BoardID:     b.boardID,
			Priority:    1,
			Assignee:    strconv.FormatInt(c.Sender().ID, 10),
			Labels:      []string{},
			CreatedAt:   time.Now(),
		}

		// Отправляем задачу в Yougile
		if err := b.yougileClient.CreateTask(task); err != nil {
			log.Printf("Ошибка создания задачи в Yougile: %v", err)
			return c.Send("Произошла ошибка при создании задачи. Пожалуйста, попробуйте позже.")
		}

		// Добавляем фотографию как вложение
		attachment := &models.Attachment{
			ID:        fmt.Sprintf("img_%d.jpg", time.Now().Unix()),
			Type:      models.AttachmentTypeImage,
			FileID:    photo.FileID,
			CreatedAt: time.Now(),
		}

		if err := b.yougileClient.UploadAttachment(task.ID, attachment, fileData); err != nil {
			log.Printf("Ошибка загрузки файла в Yougile: %v", err)
			// Даже если произошла ошибка загрузки файла, мы продолжаем, так как задача уже создана
		}

		// Сохраняем задачу локально
		b.storage.AddTask(task)
		if err := b.storage.SaveData(); err != nil {
			log.Printf("Ошибка сохранения задачи: %v", err)
		}

		// Запускаем проверку создания задачи
		b.startTaskVerification(*task, *user, caption, true, fileData)

		delete(b.taskCreationStates, c.Sender().ID)
		if err := c.Send("Задача с фотографией отправлена на создание. Вы получите уведомление после её успешного создания.", mainMenu); err != nil {
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

		// Создаем вложение
		attachment := &models.Attachment{
			ID:        fmt.Sprintf("img_%d.jpg", time.Now().Unix()),
			Type:      models.AttachmentTypeImage,
			FileID:    photo.FileID,
			CreatedAt: time.Now(),
		}

		// Загружаем файл в Yougile
		if err := b.yougileClient.UploadAttachment(taskID, attachment, fileData); err != nil {
			log.Printf("Ошибка загрузки файла в Yougile: %v", err)
			if err2 := c.Send("Ошибка при сохранении фотографии."); err2 != nil {
				log.Printf("Ошибка отправки сообщения об ошибке пользователю: %v", err2)
			}
			return nil
		}

		// Создаем комментарий с вложением
		caption := c.Message().Caption
		if caption == "" {
			caption = "[Фотография]"
		}

		comment := &models.Comment{
			TaskID:      taskID,
			AuthorID:    strconv.FormatInt(c.Sender().ID, 10),
			Text:        caption,
			Attachments: []models.Attachment{*attachment},
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		}

		// Добавляем комментарий к задаче
		if err := b.yougileClient.AddComment(taskID, comment); err != nil {
			log.Printf("Ошибка добавления комментария: %v", err)
			if err2 := c.Send("Ошибка при добавлении комментария с фотографией."); err2 != nil {
				log.Printf("Ошибка отправки сообщения об ошибке пользователю: %v", err2)
			}
			return nil
		}

		delete(b.commentStates, c.Sender().ID)
		if err := c.Send("Фотография успешно добавлена к задаче.", mainMenu); err != nil {
			log.Printf("Ошибка отправки подтверждения пользователю: %v", err)
		}
		return nil
	}

	if err := c.Send("Пожалуйста, сначала начните создание новой задачи или выберите задачу для комментирования.", mainMenu); err != nil {
		log.Printf("Ошибка отправки подсказки пользователю: %v", err)
	}
	return nil
}
