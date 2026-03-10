// Package bot содержит управление пользователями и роли.
package bot

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"yougile_bot4/internal/models"

	"gopkg.in/telebot.v3"
)

// Кнопки меню управления пользователями
var (
	userManageMenu = &telebot.ReplyMarkup{ResizeKeyboard: true}
	btnEditRole    = userManageMenu.Text("👑 Изменить роль")
	btnEditAddress = userManageMenu.Text("🏠 Изменить адрес")
	btnEditName    = userManageMenu.Text("📝 Изменить имя")
	btnBack        = userManageMenu.Text("⬅️ Назад")
)

// AdminUserState хранит состояние редактирования пользователя администратором
type AdminUserState struct {
	UserID    int64
	Action    string // "edit_role", "edit_address", "edit_name"
	Stage     string // "waiting_building", "waiting_room", "waiting_firstname", "waiting_lastname"
	StartTime time.Time
}

// handleListUsers показывает список всех пользователей с кнопками управления
func (b *Bot) handleListUsers(c telebot.Context) error {
	// Проверяем права администратора
	admin, exists := b.storage.GetUser(c.Sender().ID)
	if !exists || admin.Role != models.RoleAdmin {
		return c.Send("Эта команда доступна только администраторам.")
	}

	// Получаем список всех пользователей
	users := b.storage.GetAllUsers()
	if len(users) == 0 {
		return c.Send("Список пользователей пуст.")
	}

	// Создаем инлайн клавиатуру
	menu := &telebot.ReplyMarkup{}
	menu.Inline()

	// Добавляем кнопку для каждого пользователя
	var rows []telebot.Row
	for _, user := range users {
		roleIcon := "👤"
		if user.Role == models.RoleAdmin {
			roleIcon = "👑"
		}
		btn := menu.Data(
			fmt.Sprintf("%s %s %s (%s)", roleIcon, user.FirstName, user.LastName, user.Position),
			"select_user",
			fmt.Sprint(user.TelegramID),
		)
		rows = append(rows, menu.Row(btn))
	}
	menu.Inline(rows...)

	return c.Send("Выберите пользователя для управления:", menu)
}

// handleSelectUser обрабатывает выбор пользователя из списка
func (b *Bot) handleSelectUser(c telebot.Context) error {
	// Проверяем права администратора
	admin, exists := b.storage.GetUser(c.Sender().ID)
	if !exists || admin.Role != models.RoleAdmin {
		return c.Send("Эта команда доступна только администраторам.")
	}
	// Callback.Data может приходить в виде "select_user|<id>" или просто "<id>".
	raw := c.Callback().Data
	parts := strings.Split(raw, "|")
	var idStr string
	if len(parts) >= 2 {
		idStr = parts[len(parts)-1]
	} else {
		idStr = raw
	}

	user, exists := b.storage.GetUser(stringToInt64(idStr))
	if !exists {
		return c.Send("Пользователь не найден.")
	}

	// Сохраняем выбранного пользователя в состоянии администратора
	b.adminUserStates[c.Sender().ID] = &AdminUserState{
		UserID:    user.TelegramID,
		StartTime: time.Now(),
	}

	// Формируем сообщение с информацией о пользователе
	msg := fmt.Sprintf("📋 Информация о пользователе:\n\n"+
		"Имя: %s\n"+
		"Фамилия: %s\n"+
		"Должность: %s\n"+
		"Адрес: %s, помещение %s\n"+
		"Роль: %s\n"+
		"Статус: %s\n\n"+
		"Выберите действие:",
		user.FirstName,
		user.LastName,
		user.Position,
		user.BuildingAddress,
		user.RoomNumber,
		user.Role,
		getApprovalStatus(user.Approved))

	// Формируем inline-клавиатуру для управления выбранным пользователем
	menu := &telebot.ReplyMarkup{}
	menu.Inline()

	idStr = fmt.Sprint(user.TelegramID)
	btnEditRoleInline := menu.Data("👑 Изменить роль", "edit_role", idStr)
	btnEditAddressInline := menu.Data("🏠 Изменить адрес", "edit_address", idStr)
	btnEditNameInline := menu.Data("📝 Изменить имя", "edit_name", idStr)
	btnBackInline := menu.Data("⬅️ Назад", "back")

	menu.Inline(
		menu.Row(btnEditRoleInline),
		menu.Row(btnEditAddressInline, btnEditNameInline),
		menu.Row(btnBackInline),
	)

	return c.Edit(msg, menu)
}

// handleEditRole обрабатывает изменение роли пользователя
func (b *Bot) handleEditRole(c telebot.Context) error {
	state, exists := b.adminUserStates[c.Sender().ID]
	if !exists {
		return c.Send("Сначала выберите пользователя через /list_users")
	}

	user, exists := b.storage.GetUser(state.UserID)
	if !exists {
		return c.Send("Пользователь не найден.")
	}

	// Создаем инлайн клавиатуру для выбора роли
	menu := &telebot.ReplyMarkup{}
	menu.Inline()

	btnMakeAdmin := menu.Data("👑 Сделать администратором", "make_admin", fmt.Sprint(user.TelegramID))
	btnMakeUser := menu.Data("👤 Сделать обычным пользователем", "make_user", fmt.Sprint(user.TelegramID))

	if user.Role == models.RoleAdmin {
		menu.Inline(menu.Row(btnMakeUser))
	} else {
		menu.Inline(menu.Row(btnMakeAdmin))
	}

	return c.Edit("Выберите новую роль для пользователя:", menu)
}

// handleEditAddress начинает процесс изменения адреса
func (b *Bot) handleEditAddress(c telebot.Context) error {
	state, exists := b.adminUserStates[c.Sender().ID]
	if !exists {
		return c.Send("Сначала выберите пользователя через /list_users")
	}

	state.Action = "edit_address"
	state.Stage = "waiting_building"

	return c.Send("Введите новый адрес здания:")
}

// handleEditName начинает процесс изменения имени
func (b *Bot) handleEditName(c telebot.Context) error {
	state, exists := b.adminUserStates[c.Sender().ID]
	if !exists {
		return c.Send("Сначала выберите пользователя через /list_users")
	}

	state.Action = "edit_name"
	state.Stage = "waiting_firstname"

	return c.Send("Введите новое имя пользователя:")
}

// getApprovalStatus возвращает статус подтверждения пользователя
func getApprovalStatus(approved bool) string {
	if approved {
		return "✅ Подтвержден"
	}
	return "❌ Не подтвержден"
}

// stringToInt64 конвертирует строку в int64
func stringToInt64(s string) int64 {
	i, _ := strconv.ParseInt(s, 10, 64)
	return i
}
