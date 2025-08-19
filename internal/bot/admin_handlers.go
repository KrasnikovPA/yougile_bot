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

// AdminAction представляет действие с администратором
type AdminAction struct {
	Action    string // "promote" или "demote"
	Stage     string // "waiting_input"
	StartTime time.Time
}

var (
	btnPromoteAdmin = telebot.Btn{
		Text:   "👑 Добавить администратора",
		Unique: "promote_admin_btn",
	}
	btnDemoteAdmin = telebot.Btn{
		Text:   "⬇️ Снять администратора",
		Unique: "demote_admin_btn",
	}
)

// handleAdminActions показывает кнопки управления администраторами
func (b *Bot) handleAdminActions(c telebot.Context) error {
	sender, exists := b.storage.GetUser(c.Sender().ID)
	if !exists || sender.Role != models.RoleAdmin {
		return c.Send("Эта команда доступна только администраторам.")
	}

	menu := &telebot.ReplyMarkup{ResizeKeyboard: true}
	menu.Reply(
		menu.Row(btnPromoteAdmin),
		menu.Row(btnDemoteAdmin),
		menu.Row(btnBack),
	)

	return c.Send("Выберите действие:", menu)
}

// handlePromoteAdminButton обрабатывает нажатие кнопки добавления администратора
func (b *Bot) handlePromoteAdminButton(c telebot.Context) error {
	sender, exists := b.storage.GetUser(c.Sender().ID)
	if !exists || sender.Role != models.RoleAdmin {
		return c.Send("Эта команда доступна только администраторам.")
	}

	b.adminActions[c.Sender().ID] = &AdminAction{
		Action:    "promote",
		Stage:     "waiting_input",
		StartTime: time.Now(),
	}

	return c.Send("Введите @username или ID пользователя, которого хотите сделать администратором:")
}

// handleDemoteAdminButton обрабатывает нажатие кнопки снятия администратора
func (b *Bot) handleDemoteAdminButton(c telebot.Context) error {
	sender, exists := b.storage.GetUser(c.Sender().ID)
	if !exists || sender.Role != models.RoleAdmin {
		return c.Send("Эта команда доступна только администраторам.")
	}

	b.adminActions[c.Sender().ID] = &AdminAction{
		Action:    "demote",
		Stage:     "waiting_input",
		StartTime: time.Now(),
	}

	return c.Send("Введите @username или ID пользователя, с которого хотите снять права администратора:")
}

// handlePromoteAdmin обрабатывает команду повышения пользователя до администратора
func (b *Bot) handlePromoteAdmin(c telebot.Context) error {
	sender, exists := b.storage.GetUser(c.Sender().ID)
	if !exists || sender.Role != models.RoleAdmin {
		return c.Send("Эта команда доступна только администраторам.")
	}

	args := strings.Split(c.Message().Text, " ")
	if len(args) != 2 {
		return c.Send("Используйте команду так: /promote_admin @username или /promote_admin user_id")
	}

	var targetID int64
	if strings.HasPrefix(args[1], "@") {
		username := strings.TrimPrefix(args[1], "@")
		targetID = b.storage.GetUserIDByUsername(username)
		if targetID == 0 {
			return c.Send("Пользователь с таким username не найден.")
		}
	} else {
		var err error
		targetID, err = strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			return c.Send("Неверный формат ID пользователя.")
		}
	}

	targetUser, exists := b.storage.GetUser(targetID)
	if !exists {
		return c.Send("Пользователь не найден.")
	}

	if targetUser.Role == models.RoleAdmin {
		return c.Send(fmt.Sprintf("Пользователь %s %s уже является администратором.",
			targetUser.FirstName, targetUser.LastName))
	}

	targetUser.Role = models.RoleAdmin
	b.storage.UpdateUser(targetUser)
	log.Printf("Пользователь %d назначен администратором", targetUser.TelegramID)

	if err := b.storage.SaveData(); err != nil {
		log.Printf("Ошибка сохранения данных: %v", err)
		return c.Send("Произошла ошибка при сохранении изменений.")
	}

	if _, err := b.bot.Send(&telebot.User{ID: targetID}, "Вам были предоставлены права администратора."); err != nil {
		log.Printf("Ошибка отправки уведомления пользователю %d: %v", targetID, err)
	}

	return c.Send(fmt.Sprintf("Пользователь %s %s назначен администратором.",
		targetUser.FirstName, targetUser.LastName))
}

// handleDemoteAdmin обрабатывает команду снятия прав администратора
func (b *Bot) handleDemoteAdmin(c telebot.Context) error {
	sender, exists := b.storage.GetUser(c.Sender().ID)
	if !exists || sender.Role != models.RoleAdmin {
		return c.Send("Эта команда доступна только администраторам.")
	}

	args := strings.Split(c.Message().Text, " ")
	if len(args) != 2 {
		return c.Send("Используйте команду так: /demote_admin @username или /demote_admin user_id")
	}

	var targetID int64
	if strings.HasPrefix(args[1], "@") {
		username := strings.TrimPrefix(args[1], "@")
		targetID = b.storage.GetUserIDByUsername(username)
		if targetID == 0 {
			return c.Send("Пользователь с таким username не найден.")
		}
	} else {
		var err error
		targetID, err = strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			return c.Send("Неверный формат ID пользователя.")
		}
	}

	targetUser, exists := b.storage.GetUser(targetID)
	if !exists {
		return c.Send("Пользователь не найден.")
	}

	if targetUser.Role != models.RoleAdmin {
		return c.Send(fmt.Sprintf("Пользователь %s %s не является администратором.",
			targetUser.FirstName, targetUser.LastName))
	}

	targetUser.Role = models.RoleUser
	b.storage.UpdateUser(targetUser)
	log.Printf("С пользователя %d сняты права администратора", targetUser.TelegramID)

	if err := b.storage.SaveData(); err != nil {
		log.Printf("Ошибка сохранения данных: %v", err)
		return c.Send("Произошла ошибка при сохранении изменений.")
	}

	if _, err := b.bot.Send(&telebot.User{ID: targetID}, "С вас были сняты права администратора."); err != nil {
		log.Printf("Ошибка отправки уведомления пользователю %d: %v", targetID, err)
	}

	return c.Send(fmt.Sprintf("С пользователя %s %s сняты права администратора.",
		targetUser.FirstName, targetUser.LastName))
}
