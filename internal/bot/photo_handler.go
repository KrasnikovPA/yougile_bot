// Package bot обрабатывает получаемые от пользователей фотографии и вложения.
package bot

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
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

		// Подготовим идентификатор файла и FileID для комментария.
		//
		// Примечание: в текущей версии мы сохраняем фотографию локально и добавляем текстовую
		// ссылку в комментарий задачи вместо прямой загрузки/прикрепления к Yougile.
		// На практике это сделано потому, что интеграция загрузки вложений в API Yougile
		// пока не реализована/не надёжна для этого инстанса — поэтому сохраняем копию
		// на диск (`data/uploads`) и оставляем Telegram FileID в комментарии на случай,
		// если позже захотим подтянуть файл из Telegram или реализовать uploadAttachment.
		//
		// Чтобы вернуть полноценную загрузку вложений, нужно реализовать в `internal/api`
		// метод UploadAttachment(filePath string, taskID string) error и вызвать его здесь.
		attID := fmt.Sprintf("img_%d.jpg", time.Now().Unix())
		fileID := photo.FileID

		// Save file locally and add a textual comment to the task with reference to the saved file
		uploadsDir := "data/uploads"
		if err := os.MkdirAll(uploadsDir, 0755); err != nil {
			log.Printf("Ошибка создания каталога для загрузок: %v", err)
		}
		savedPath := filepath.Join(uploadsDir, attID)
		if werr := os.WriteFile(savedPath, fileData, 0644); werr != nil {
			log.Printf("Ошибка сохранения файла локально: %v", werr)
		} else {
			// create a comment referencing the saved file and telegram file id
			commentText := fmt.Sprintf("%s\n[Фотография сохранена локально: %s]\n[Telegram FileID: %s]", caption, savedPath, fileID)
			comment := &models.Comment{
				TaskID:    task.ID,
				AuthorID:  strconv.FormatInt(c.Sender().ID, 10),
				Text:      commentText,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			taskIDStr := task.ExternalID
			if taskIDStr == "" {
				taskIDStr = strconv.FormatInt(task.ID, 10)
			}
			if cerr := b.yougileClient.AddComment(taskIDStr, comment); cerr != nil {
				log.Printf("Ошибка добавления комментария к задаче (локальная ссылка): %v", cerr)
			} else {
				// persist locally
				b.storage.AddTask(task)
				task.Comments = append(task.Comments, *comment)
				b.storage.UpdateTask(task)
				// ensure we also update any cached tasks list
				tasks := b.storage.GetTasks()
				for _, t := range tasks {
					if t.ID == task.ID {
						t.Comments = append(t.Comments, *comment)
						b.storage.UpdateTask(t)
						break
					}
				}
				if sErr := b.storage.SaveData(); sErr != nil {
					log.Printf("Ошибка сохранения задачи с комментарием: %v", sErr)
				}
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

		// Подготовим идентификатор файла и FileID для комментария (не используем загрузку/attach в Yougile сейчас)
		attID := fmt.Sprintf("img_%d.jpg", time.Now().Unix())
		fileID := photo.FileID

		// Save file locally and add comment referencing it
		uploadsDir := "data/uploads"
		if err := os.MkdirAll(uploadsDir, 0755); err != nil {
			log.Printf("Ошибка создания каталога для загрузок: %v", err)
		}
		savedPath := filepath.Join(uploadsDir, attID)
		if werr := os.WriteFile(savedPath, fileData, 0644); werr != nil {
			log.Printf("Ошибка сохранения файла локально: %v", werr)
			if err2 := c.Send("Ошибка при сохранении фотографии."); err2 != nil {
				log.Printf("Ошибка отправки сообщения об ошибке пользователю: %v", err2)
			}
			return nil
		}

		// Создаем комментарий с вложением (текстовая ссылка на локальную копию и Telegram FileID)
		caption := c.Message().Caption
		if caption == "" {
			caption = "[Фотография]"
		}

		commentText := fmt.Sprintf("%s\n[Фотография сохранена локально: %s]\n[Telegram FileID: %s]", caption, savedPath, fileID)

		comment := &models.Comment{
			TaskID:    taskID,
			AuthorID:  strconv.FormatInt(c.Sender().ID, 10),
			Text:      commentText,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}

		// Добавляем комментарий к задаче
		// Add comment to Yougile using string id: prefer ExternalID from storage
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
		if err := c.Send("Фотография успешно обработана и ссылка добавлена в комментарий задачи.", b.menuForContext(c)); err != nil {
			log.Printf("Ошибка отправки подтверждения пользователю: %v", err)
		}
		return nil
	}

	if err := c.Send("Пожалуйста, сначала начните создание новой задачи или выберите задачу для комментирования.", b.menuForContext(c)); err != nil {
		log.Printf("Ошибка отправки подсказки пользователю: %v", err)
	}
	return nil
}
