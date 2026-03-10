// Package bot содержит реализацию Telegram-бота и обработчиков команд.
package bot

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"yougile_bot4/internal/api"
	"yougile_bot4/internal/metrics"
	"yougile_bot4/internal/models"
	"yougile_bot4/internal/storage"

	"gopkg.in/telebot.v3"
)

// Кнопки основного меню
var (
	// Global menus for users and admins
	mainMenuUser  = &telebot.ReplyMarkup{ResizeKeyboard: true}
	mainMenuAdmin = &telebot.ReplyMarkup{ResizeKeyboard: true}

	btnHelp    = mainMenuUser.Text("❓ Помощь")
	btnNewTask = mainMenuUser.Text("📝 Новая задача")
	btnFAQ     = mainMenuUser.Text("ℹ️ Частые вопросы")
	// Admin-specific buttons
	btnAddress = mainMenuAdmin.Text("🏠 Изменить адрес")
	btnUsers   = mainMenuAdmin.Text("👥 Пользователи")

	// Кнопки для администратора
	adminMenu  = &telebot.ReplyMarkup{ResizeKeyboard: true}
	btnApprove = adminMenu.Text("✅ Подтвердить")
	btnReject  = adminMenu.Text("❌ Отклонить")

	// Кнопка пропуска комментария
	commentMenu = &telebot.ReplyMarkup{ResizeKeyboard: true}
	btnSkip     = commentMenu.Text("⏭ Без комментария")
)

// Используем TaskCreationState из models

// RegistrationState хранит состояние процесса регистрации пользователя в боте.
// Содержит временную информацию и текущий этап заполнения данных.
type RegistrationState struct {
	StartTime time.Time
	User      *models.User
	Stage     string // "waiting_firstname", "waiting_lastname", "waiting_building", "waiting_room"
}

// Bot представляет Telegram-бота и его внутреннее состояние.
// Оборачивает telebot.Bot и содержит ссылки на хранилище, API-клиент и метрики.
type Bot struct {
	bot                *telebot.Bot
	storage            *storage.Storage
	yougileClient      *api.Client
	boardID            string
	regTimeout         time.Duration
	minMsgLen          int
	notifications      chan string
	metrics            *metrics.Metrics                    // метрики бота
	regStates          map[int64]*RegistrationState        // ключ - TelegramID
	addressChange      map[int64]string                    // этап изменения адреса: "building" или "room"
	taskCreationStates map[int64]*models.TaskCreationState // состояния создания задач
	commentStates      map[int64]int64                     // ожидание комментария: ключ - TelegramID, значение - TaskID
	timeStates         map[int64]int64                     // ожидание времени: ключ - TelegramID, значение - TaskID
	pendingReqs        map[int64]*models.PendingRequest    // запросы, ожидающие подтверждения
	adminActions       map[int64]*AdminAction              // состояния действий администратора
	adminUserStates    map[int64]*AdminUserState           // состояния управления пользователями
	defaultColumn      string
	// full scan control
	fullScanCancel  context.CancelFunc
	fullScanMu      sync.Mutex
	fullScanRunning bool
}

// NewBot создает и настраивает экземпляр Bot, регистрирует обработчики команд.
func NewBot(token string, storage *storage.Storage, yougileToken string, boardID string, regTimeout time.Duration, minMsgLen int, metrics *metrics.Metrics) (*Bot, error) {
	b, err := telebot.NewBot(telebot.Settings{
		Token:  token,
		Poller: &telebot.LongPoller{Timeout: 10 * time.Second},
	})
	if err != nil {
		return nil, fmt.Errorf("ошибка создания бота: %w", err)
	}

	yougileClient := api.NewClient(yougileToken, boardID, 30*time.Second, metrics)

	bot := &Bot{
		bot:                b,
		storage:            storage,
		yougileClient:      yougileClient,
		boardID:            boardID,
		regTimeout:         regTimeout,
		minMsgLen:          minMsgLen,
		notifications:      make(chan string, 100),
		metrics:            metrics,
		regStates:          make(map[int64]*RegistrationState),
		addressChange:      make(map[int64]string),
		pendingReqs:        make(map[int64]*models.PendingRequest),
		adminActions:       make(map[int64]*AdminAction),
		taskCreationStates: make(map[int64]*models.TaskCreationState),
		commentStates:      make(map[int64]int64),
		timeStates:         make(map[int64]int64),
		adminUserStates:    make(map[int64]*AdminUserState),
		defaultColumn:      os.Getenv("COLUMN_ID"),
	}

	// Настраиваем клавиатуру для основного меню
	mainMenuUser.Reply(
		mainMenuUser.Row(btnNewTask),
		mainMenuUser.Row(btnHelp),
		mainMenuUser.Row(btnFAQ),
	)

	mainMenuAdmin.Reply(
		mainMenuAdmin.Row(btnNewTask),
		mainMenuAdmin.Row(btnHelp),
		mainMenuAdmin.Row(btnUsers),
		mainMenuAdmin.Row(btnFAQ),
	)

	// Настраиваем клавиатуру для администратора
	adminMenu.Reply(
		adminMenu.Row(btnApprove, btnReject),
	)

	// Кнопка для просмотра пользователей (для админов)
	// Регистрация обработчика производится в setupHandlers

	bot.setupHandlers()
	return bot, nil
}

// menuForUserID возвращает подходящее главное меню для пользователя по его TelegramID.
func (b *Bot) menuForUserID(id int64) interface{} {
	if u, ok := b.storage.GetUser(id); ok {
		if u.Role == models.RoleAdmin {
			return mainMenuAdmin
		}
	}
	return mainMenuUser
}

// menuForContext возвращает меню для пользователя из telebot.Context.
func (b *Bot) menuForContext(c telebot.Context) interface{} {
	if c == nil || c.Sender() == nil {
		return mainMenuUser
	}
	return b.menuForUserID(c.Sender().ID)
}

// formatTaskDescription формирует описание задачи и в конце добавляет в скобках
// адрес, кабинет и должность; имя/фамилия не добавляются (они будут в заголовке).
func (b *Bot) formatTaskDescription(user *models.User, original string) string {
	// Собираем постфикс (адрес, кабинет, должность)
	parts := []string{}
	if user.BuildingAddress != "" {
		parts = append(parts, user.BuildingAddress)
	} else if user.Address != "" {
		parts = append(parts, user.Address)
	}
	if user.RoomNumber != "" {
		parts = append(parts, "каб. "+user.RoomNumber)
	}
	if user.Position != "" {
		parts = append(parts, user.Position)
	}
	postfix := ""
	if len(parts) > 0 {
		postfix = " (" + strings.Join(parts, ", ") + ")"
	}

	original = strings.TrimSpace(original)
	if original == "" {
		return strings.TrimSpace(postfix)
	}
	return original + postfix
}

// formatTaskTitle добавляет имя и фамилию в начало заголовка задачи: "Имя Фамилия. <Title>"
func (b *Bot) formatTaskTitle(user *models.User, title string) string {
	name := strings.TrimSpace(user.FirstName + " " + user.LastName)
	if name == "" {
		return strings.TrimSpace(title)
	}
	if title == "" {
		return name + "."
	}
	return name + ". " + strings.TrimSpace(title)
}

// RescanTasks выполняет немедленную проверку задач через API Yougile и
// отправляет уведомления для новых задач. Возвращает ошибку при неудаче.
func (b *Bot) RescanTasks(limit int) error {
	tasks, err := b.yougileClient.GetTasks(limit)
	if err != nil {
		return fmt.Errorf("RescanTasks: GetTasks failed: %w", err)
	}

	for _, task := range tasks {
		key := ""
		if task.ExternalID != "" {
			key = task.ExternalID
		} else if task.Key != "" {
			key = task.Key
		} else if task.ID != 0 {
			key = fmt.Sprintf("%d", task.ID)
		}
		if key == "" {
			continue
		}
		known := b.storage.IsKnownKey(key)
		log.Printf("RescanTasks: task Key=%s ID=%d ExternalID=%s Title=%q Done=%v Known=%v", key, task.ID, task.ExternalID, task.Title, task.Done, known)
		if !known {
			b.storage.AddKnownKey(key)
			if task.ID != 0 {
				b.storage.AddKnownTask(task.ID)
			}
			if !task.Done {
				b.SendNotification(b.formatTaskNotification(task))
			}
		}
	}
	return nil
}

// formatTaskNotification формирует текст уведомления о задаче (аналогично реализации в main).
func (b *Bot) formatTaskNotification(task models.Task) string {
	var status, priority string

	if task.Done {
		status = "✅"
	} else {
		status = "🔵"
	}

	switch task.Priority {
	case 1:
		priority = "⚡️ Высокий"
	case 2:
		priority = "⭐️ Средний"
	default:
		priority = "📌 Обычный"
	}

	var dueDate string
	if !task.DueDate.IsZero() {
		dueDate = fmt.Sprintf("\n📅 Срок: %s", task.DueDate.Format("02.01.2006"))
	}

	var assignee string
	if task.Assignee != "" {
		assignee = fmt.Sprintf("\n👤 Исполнитель: %s", task.Assignee)
	}

	msg := fmt.Sprintf("%s Новая задача\n"+
		"📎 %s\n"+
		"🏷 %s%s%s",
		status, task.Title, priority, dueDate, assignee)

	if task.Description != "" {
		descLen := len(task.Description)
		if descLen > 200 {
			descLen = 200
		}
		msg += fmt.Sprintf("\n\n📝 %s", task.Description[:descLen])
		if len(task.Description) > 200 {
			msg += "..."
		}
	}

	return msg
}

// setupHandlers настраивает обработчики команд
func (b *Bot) setupHandlers() {
	// Стандартные команды
	b.bot.Handle("/start", b.handleStart)
	b.bot.Handle("/help", b.handleHelp)
	b.bot.Handle("/address", b.handleChangeAddress)
	// Команда для создания новой задачи через конструктор
	b.bot.Handle("/newtask", b.handleTaskConstructor)

	// Admin commands to manage notification target chat IDs
	b.bot.Handle("/addadmin", func(c telebot.Context) error {
		sender, exists := b.storage.GetUser(c.Sender().ID)
		if !exists || sender.Role != models.RoleAdmin {
			return c.Send("Команда доступна только администраторам.")
		}
		arg := strings.TrimSpace(strings.TrimPrefix(c.Text(), "/addadmin"))
		var chatID int64
		if arg == "" {
			chatID = c.Sender().ID
		} else {
			v, err := strconv.ParseInt(arg, 10, 64)
			if err != nil {
				return c.Send("Неверный формат chat id. Использование: /addadmin <chatid> или просто /addadmin чтобы добавить текущий чат")
			}
			chatID = v
		}
		b.storage.AddChatID(chatID)
		if err := b.storage.SaveData(); err != nil {
			log.Printf("Ошибка сохранения chat_ids: %v", err)
		}
		return c.Send(fmt.Sprintf("Добавлен chat id для уведомлений: %d", chatID))
	})

	b.bot.Handle("/listadmins", func(c telebot.Context) error {
		sender, exists := b.storage.GetUser(c.Sender().ID)
		if !exists || sender.Role != models.RoleAdmin {
			return c.Send("Команда доступна только администраторам.")
		}
		chats := b.storage.GetChatIDs()
		if len(chats) == 0 {
			return c.Send("Список chat_id пуст. Добавьте текущий чат: /addadmin")
		}
		var sb strings.Builder
		sb.WriteString("Зарегистрированные chat_id:\n")
		for _, id := range chats {
			sb.WriteString(fmt.Sprintf("- %d\n", id))
		}
		return c.Send(sb.String())
	})

	// Обработчики кнопок
	b.bot.Handle(&btnHelp, b.handleHelp)
	b.bot.Handle(&btnAddress, b.handleChangeAddress)
	b.bot.Handle(&btnUsers, b.handleListUsers)
	b.bot.Handle(&btnApprove, b.handleApprove)
	b.bot.Handle(&btnReject, b.handleReject)
	b.bot.Handle(&btnFAQ, b.handleFAQ)

	// Команды администратора
	b.bot.Handle("/admin", b.handleAdminActions)
	b.bot.Handle("/promote_admin", b.handlePromoteAdmin)
	b.bot.Handle("/demote_admin", b.handleDemoteAdmin)
	b.bot.Handle("/list_users", b.handleListUsers)
	// Full scan commands (admins only)
	b.bot.Handle("/fullscan", func(c telebot.Context) error {
		sender, exists := b.storage.GetUser(c.Sender().ID)
		if !exists || sender.Role != models.RoleAdmin {
			return c.Send("Команда доступна только администраторам.")
		}
		// optional arg: range
		arg := strings.TrimSpace(strings.TrimPrefix(c.Text(), "/fullscan"))
		rng := 100
		if arg != "" {
			if v, err := strconv.Atoi(arg); err == nil && v > 0 {
				rng = v
			}
		}
		if err := b.startFullScan(rng); err != nil {
			return c.Send(fmt.Sprintf("Не удалось запустить fullscan: %v", err))
		}
		return c.Send(fmt.Sprintf("Full scan запущен (%d запросов)", rng))
	})
	b.bot.Handle("/stopfullscan", func(c telebot.Context) error {
		sender, exists := b.storage.GetUser(c.Sender().ID)
		if !exists || sender.Role != models.RoleAdmin {
			return c.Send("Команда доступна только администраторам.")
		}
		if err := b.stopFullScan(); err != nil {
			return c.Send(fmt.Sprintf("Не удалось остановить fullscan: %v", err))
		}
		return c.Send("Full scan остановлен")
	})

	// Обработчики кнопок управления пользователями
	b.bot.Handle(&btnPromoteAdmin, b.handlePromoteAdminButton)
	b.bot.Handle(&btnDemoteAdmin, b.handleDemoteAdminButton)
	b.bot.Handle(&btnEditRole, b.handleEditRole)
	b.bot.Handle(&btnEditAddress, b.handleEditAddress)
	b.bot.Handle(&btnEditName, b.handleEditName)
	b.bot.Handle(&btnBack, b.handleListUsers)

	// Callback-обработчики
	b.bot.Handle(telebot.OnCallback, func(c telebot.Context) error {
		// Debug log: показать все входящие callback'ы и очищённую data
		if c != nil && c.Callback() != nil {
			raw := c.Callback().Data
			sanitized := strings.TrimSpace(raw)
			log.Printf("Callback received: unique=%s raw=%q sanitized=%q from=%d", c.Callback().Unique, raw, sanitized, c.Sender().ID)

			// Используем sanitized для всех проверок
			data := sanitized

			if c.Callback().Unique == "faq" {
				return b.handleFAQCallback(c)
			}

			// Обработка FAQ в формате data: faq|key
			if strings.HasPrefix(data, "faq|") {
				// извлечь ключ и положить в Callback.Data
				key := strings.TrimPrefix(data, "faq|")
				c.Callback().Data = key
				return b.handleFAQCallback(c)
			}

			if strings.HasPrefix(data, "task_step|") {
				// Подменим Callback.Data на очищенную версию для обработчика
				c.Callback().Data = data
				return b.handleTaskStepCallback(c)
			}

			if strings.HasPrefix(data, "task_select|") {
				c.Callback().Data = data
				return b.handleTaskSelectCallback(c)
			}

			if strings.HasPrefix(data, "select_user|") {
				c.Callback().Data = data
				return b.handleSelectUser(c)
			}
			if strings.HasPrefix(data, "make_admin|") {
				c.Callback().Data = data
				return b.handleMakeAdminCallback(c)
			}
			if strings.HasPrefix(data, "make_user|") {
				c.Callback().Data = data
				return b.handleMakeUserCallback(c)
			}

			if strings.HasPrefix(data, "edit_role|") {
				c.Callback().Data = data
				return b.handleEditRole(c)
			}
			if strings.HasPrefix(data, "edit_address|") {
				c.Callback().Data = data
				return b.handleEditAddress(c)
			}
			if strings.HasPrefix(data, "edit_name|") {
				c.Callback().Data = data
				return b.handleEditName(c)
			}
			if strings.HasPrefix(data, "back") {
				// Возврат к списку пользователей
				return b.handleListUsers(c)
			}

			// Обработка approve/reject от inline-кнопок для регистрации и изменений адреса
			if strings.HasPrefix(data, "approve|") {
				c.Callback().Data = data
				return b.handleApprove(c)
			}
			if strings.HasPrefix(data, "reject|") {
				c.Callback().Data = data
				return b.handleReject(c)
			}
		} else {
			log.Printf("Callback received: c or c.Callback is nil")
		}

		switch c.Callback().Data {
		case "select_user":
			return b.handleSelectUser(c)
		case "make_admin":
			return b.handlePromoteAdmin(c)
		case "make_user":
			return b.handleDemoteAdmin(c)
		}
		return nil
	})

	// Обработчики задач
	b.bot.Handle(&btnNewTask, b.handleTaskConstructor) // Используем конструктор вместо простого создания
	b.bot.Handle(&btnSkip, b.handleSkip)

	// Команда для немедленной проверки новых задач (только для админов)
	b.bot.Handle("/rescan", func(c telebot.Context) error {
		sender, exists := b.storage.GetUser(c.Sender().ID)
		if !exists || sender.Role != models.RoleAdmin {
			return c.Send("Команда доступна только администраторам.")
		}
		// Выполним сканирование
		if err := b.RescanTasks(100); err != nil {
			log.Printf("rescan: error: %v", err)
			return c.Send(fmt.Sprintf("Ошибка при сканировании: %v", err))
		}
		return c.Send("Рескан завершён. Проверьте логи для деталей.")
	})

	// Команда для поиска конкретной задачи по ключу/ID (админам)
	b.bot.Handle("/findtask", func(c telebot.Context) error {
		admin, exists := b.storage.GetUser(c.Sender().ID)
		if !exists || admin.Role != models.RoleAdmin {
			return c.Send("Команда доступна только администраторам.")
		}
		// Получаем аргумент после команды
		args := strings.TrimSpace(strings.TrimPrefix(c.Text(), "/findtask"))
		if args == "" {
			return c.Send("Использование: /findtask <ключ_или_id>")
		}
		key := args
		// Попытаемся получить задачу по ID/ключу
		task, err := b.yougileClient.GetTaskByID(key)
		if err != nil {
			log.Printf("findtask: GetTaskByID(%s) error: %v", key, err)
			return c.Send(fmt.Sprintf("Ошибка при запросе задачи: %v", err))
		}
		if task == nil {
			return c.Send("Задача не найдена через API Yougile.")
		}
		// Формируем краткий ответ
		resp := fmt.Sprintf("Найдена задача:\nID=%d\nExternalID=%s\nKey=%s\nTitle=%s\nDone=%v\nBoard=%s\nColumn=%s", task.ID, task.ExternalID, task.Key, task.Title, task.Done, task.BoardID, task.ColumnID)
		return c.Send(resp)
	})

	// Admin helper: force notify about a task by key (marks as known and sends notification)
	b.bot.Handle("/notify", func(c telebot.Context) error {
		sender, exists := b.storage.GetUser(c.Sender().ID)
		if !exists || sender.Role != models.RoleAdmin {
			return c.Send("Команда доступна только администраторам.")
		}
		args := strings.TrimSpace(strings.TrimPrefix(c.Text(), "/notify"))
		if args == "" {
			return c.Send("Использование: /notify <ключ_или_id> — пометить задачу как новую и разослать уведомление")
		}
		key := args
		task, err := b.yougileClient.GetTaskByID(key)
		if err != nil {
			log.Printf("notify: GetTaskByID(%s) error: %v", key, err)
			return c.Send(fmt.Sprintf("Ошибка при запросе задачи: %v", err))
		}
		if task == nil {
			return c.Send("Задача не найдена через API Yougile.")
		}

		// Determine tracking key
		tkey := ""
		if task.ExternalID != "" {
			tkey = task.ExternalID
		} else if task.Key != "" {
			tkey = task.Key
		} else if task.ID != 0 {
			tkey = fmt.Sprintf("%d", task.ID)
		}
		if tkey == "" {
			return c.Send("Не удалось определить ключ задачи для отслеживания.")
		}

		// Mark known and persist
		if !b.storage.IsKnownKey(tkey) {
			b.storage.AddKnownKey(tkey)
		}
		if task.ID != 0 && !b.storage.IsKnownTask(task.ID) {
			b.storage.AddKnownTask(task.ID)
		}
		if err := b.storage.SaveData(); err != nil {
			log.Printf("notify: error saving storage: %v", err)
		}

		// Send notification if task not done
		if !task.Done {
			chats := b.storage.GetChatIDs()
			log.Printf("notify: sending notification for %s to %d chats", tkey, len(chats))
			b.SendNotification(b.formatTaskNotification(*task))
			log.Printf("notify: SendNotification called for %s", tkey)
			return c.Send(fmt.Sprintf("Уведомление отправлено для %s (чатов: %d)", tkey, len(chats)))
		}
		return c.Send("Задача помечена как известная, но не отправлено уведомление — задача помечена как завершённая/удалённая.")
	})

	// Обработчик текстовых сообщений
	b.bot.Handle(telebot.OnText, b.handleMessage)

	// Обработчик фотографий
	b.bot.Handle(telebot.OnPhoto, b.handlePhoto)

	// Обработчик документов и файлов
	b.bot.Handle(telebot.OnDocument, b.handleDocument)
}

// handleStart обрабатывает команду /start
func (b *Bot) handleStart(c telebot.Context) error {
	// Проверяем, не зарегистрирован ли уже пользователь
	if user, exists := b.storage.GetUser(c.Sender().ID); exists {
		if user.Approved {
			return c.Send("Вы уже зарегистрированы и подтверждены в системе.", b.menuForContext(c))
		}
		return c.Send("Ваша заявка на регистрацию уже находится на рассмотрении.")
	}

	// Проверяем, есть ли уже администраторы
	isFirstUser := !b.storage.HasAdmins()

	user := &models.User{
		TelegramID: c.Sender().ID,
		Role:       models.RoleUser, // Роль будет автоматически изменена в AddUser, если это первый пользователь
		Approved:   false,
		Username:   c.Sender().Username, // Сохраняем username пользователя
	}

	b.regStates[c.Sender().ID] = &RegistrationState{
		StartTime: time.Now(),
		User:      user,
		Stage:     "waiting_firstname",
	}

	if isFirstUser {
		return c.Send("Добро пожаловать! Вы будете назначены первым администратором системы.\nПожалуйста, введите ваше имя.")
	}
	return c.Send("Добро пожаловать! Пожалуйста, введите ваше имя.")
}

// handleHelp обрабатывает команду /help
func (b *Bot) handleHelp(c telebot.Context) error {
	_, exists := b.storage.GetUser(c.Sender().ID)
	if !exists {
		return c.Send(`Доступные команды:
/start - Начать работу с ботом
/help - Показать это сообщение`)
	}

	return c.Send(`Доступные команды:
	/start - Начать работу с ботом
	/help - Показать это сообщение
	/address - Изменить ваш адрес`, b.menuForContext(c))
}

// handleChangeAddress обрабатывает команду изменения адреса
func (b *Bot) handleChangeAddress(c telebot.Context) error {
	user, exists := b.storage.GetUser(c.Sender().ID)
	if !exists {
		return c.Send("Пожалуйста, сначала зарегистрируйтесь с помощью команды /start")
	}

	if !user.Approved {
		return c.Send("Ваш аккаунт еще не подтвержден администратором.")
	}

	if user.AddressChange {
		return c.Send("У вас уже есть запрос на изменение адреса. Дождитесь подтверждения администратора.")
	}

	b.addressChange[c.Sender().ID] = "building"
	return c.Send("Пожалуйста, введите адрес здания (например: ул. Ленина, 1).")
}

// handleMessage обрабатывает текстовые сообщения
func (b *Bot) handleMessage(c telebot.Context) error {
	// Обработка состояний администратора при редактировании пользователя (имя/адрес)
	if state, ok := b.adminUserStates[c.Sender().ID]; ok {
		// Проверяем таймаут состояния
		if time.Since(state.StartTime) > 5*time.Minute {
			delete(b.adminUserStates, c.Sender().ID)
			return c.Send("Время ожидания истекло. Пожалуйста, начните сначала.")
		}

		user, exists := b.storage.GetUser(state.UserID)
		if !exists {
			delete(b.adminUserStates, c.Sender().ID)
			return c.Send("Пользователь больше не найден.")
		}

		switch state.Action {
		case "edit_address":
			// stage waiting_building or waiting_room
			if state.Stage == "waiting_building" {
				building := strings.TrimSpace(c.Text())
				if len(building) < 5 {
					return c.Send("Адрес здания должен содержать минимум 5 символов. Пожалуйста, укажите более подробный адрес.")
				}
				user.BuildingAddress = building
				state.Stage = "waiting_room"
				b.adminUserStates[c.Sender().ID] = state
				return c.Send("Теперь введите номер кабинета для выбранного пользователя.")
			}
			if state.Stage == "waiting_room" {
				room := strings.TrimSpace(c.Text())
				if len(room) < 1 {
					return c.Send("Пожалуйста, укажите номер кабинета.")
				}
				user.RoomNumber = room
				user.AddressChange = false
				b.storage.UpdateUser(user)
				if err := b.storage.SaveData(); err != nil {
					log.Printf("Ошибка сохранения данных при редактировании адреса: %v", err)
				}
				delete(b.adminUserStates, c.Sender().ID)
				return c.Send("Адрес пользователя обновлён.")
			}

		case "edit_name":
			if state.Stage == "waiting_firstname" {
				firstname := strings.TrimSpace(c.Text())
				if len(firstname) < 2 {
					return c.Send("Имя должно содержать минимум 2 символа. Пожалуйста, попробуйте снова.")
				}
				user.FirstName = firstname
				state.Stage = "waiting_lastname"
				b.adminUserStates[c.Sender().ID] = state
				return c.Send("Теперь введите фамилию для выбранного пользователя.")
			}
			if state.Stage == "waiting_lastname" {
				lastname := strings.TrimSpace(c.Text())
				if len(lastname) < 2 {
					return c.Send("Фамилия должна содержать минимум 2 символа. Пожалуйста, попробуйте снова.")
				}
				user.LastName = lastname
				b.storage.UpdateUser(user)
				if err := b.storage.SaveData(); err != nil {
					log.Printf("Ошибка сохранения данных при редактировании имени: %v", err)
				}
				delete(b.adminUserStates, c.Sender().ID)
				return c.Send("Имя пользователя обновлено.")
			}
		}
	}

	// Проверяем, изменяет ли пользователь адрес
	if stage, isChangingAddress := b.addressChange[c.Sender().ID]; isChangingAddress {
		input := strings.TrimSpace(c.Text())
		user, exists := b.storage.GetUser(c.Sender().ID)
		if !exists {
			delete(b.addressChange, c.Sender().ID)
			return c.Send("Ошибка: пользователь не найден")
		}

		switch stage {
		case "building":
			if len(input) < 5 {
				return c.Send("Адрес здания должен содержать минимум 5 символов. Пожалуйста, укажите более подробный адрес.")
			}
			user.BuildingAddress = input
			b.addressChange[c.Sender().ID] = "room"
			return c.Send("Теперь укажите номер кабинета.")

		case "room":
			if len(input) < 1 {
				return c.Send("Пожалуйста, укажите номер кабинета.")
			}
			user.RoomNumber = input
			user.AddressChange = true // Ожидание подтверждения администратором
			b.storage.UpdateUser(user)
			if err := b.storage.SaveData(); err != nil {
				log.Printf("Ошибка сохранения данных: %v", err)
			}

			delete(b.addressChange, c.Sender().ID)
			return c.Send("Запрос на изменение адреса отправлен. Ожидайте подтверждения администратора.")
		}
		return nil
	}

	// Проверяем, находится ли пользователь в процессе регистрации
	if regState, ok := b.regStates[c.Sender().ID]; ok {
		// Проверяем таймаут регистрации
		if time.Since(regState.StartTime) > b.regTimeout {
			delete(b.regStates, c.Sender().ID)
			return c.Send("Время регистрации истекло. Пожалуйста, используйте /start для начала регистрации заново.")
		}

		switch regState.Stage {
		case "waiting_firstname":
			firstname := strings.TrimSpace(c.Text())
			if len(firstname) < 2 {
				return c.Send("Имя должно содержать минимум 2 символа. Пожалуйста, попробуйте снова.")
			}
			regState.User.FirstName = firstname
			regState.Stage = "waiting_lastname"
			return c.Send("Отлично! Теперь введите вашу фамилию.")

		case "waiting_lastname":
			lastname := strings.TrimSpace(c.Text())
			if len(lastname) < 2 {
				return c.Send("Фамилия должна содержать минимум 2 символа. Пожалуйста, попробуйте снова.")
			}
			regState.User.LastName = lastname
			regState.Stage = "waiting_building"
			return c.Send("Хорошо! Теперь укажите адрес здания (например: ул. Ленина, 1).")

		case "waiting_building":
			building := strings.TrimSpace(c.Text())
			if len(building) < 5 {
				return c.Send("Адрес здания должен содержать минимум 5 символов. Пожалуйста, укажите более подробный адрес.")
			}
			regState.User.BuildingAddress = building
			regState.Stage = "waiting_room"
			return c.Send("Теперь укажите номер кабинета.")

		case "waiting_room":
			room := strings.TrimSpace(c.Text())
			if len(room) < 1 {
				return c.Send("Пожалуйста, укажите номер кабинета.")
			}
			regState.User.RoomNumber = room
			regState.Stage = "waiting_position"
			return c.Send("Теперь укажите вашу должность.")

		case "waiting_position":
			position := strings.TrimSpace(c.Text())
			if len(position) < 2 {
				return c.Send("Должность должна содержать минимум 2 символа. Пожалуйста, попробуйте снова.")
			}
			regState.User.Position = position
			b.storage.AddUser(regState.User)
			if err := b.storage.SaveData(); err != nil {
				log.Printf("Ошибка сохранения данных: %v", err)
			}

			// Создаем запрос на подтверждение регистрации и уведомляем администраторов
			req := &models.PendingRequest{
				UserID:    regState.User.TelegramID,
				Type:      "registration",
				CreatedAt: time.Now(),
			}
			b.pendingReqs[regState.User.TelegramID] = req

			// Формируем inline-клавиатуру для подтверждения/отклонения
			menu := &telebot.ReplyMarkup{}
			uidStr := strconv.FormatInt(regState.User.TelegramID, 10)
			btnApprove := menu.Data("✅ Подтвердить", "approve|"+uidStr)
			btnReject := menu.Data("❌ Отклонить", "reject|"+uidStr)
			menu.Inline(menu.Row(btnApprove, btnReject))

			// Отправляем уведомление всем администраторам
			for _, u := range b.storage.GetUsers() {
				if u.Role != models.RoleAdmin {
					continue
				}
				msg := fmt.Sprintf("Новая заявка на регистрацию:\n👤 %s %s (%d)\n💼 Должность: %s",
					regState.User.FirstName, regState.User.LastName, regState.User.TelegramID, regState.User.Position)
				if _, err := b.bot.Send(&telebot.User{ID: u.TelegramID}, msg, menu); err != nil {
					log.Printf("Ошибка отправки уведомления администратору %d: %v", u.TelegramID, err)
				}
			}

			delete(b.regStates, c.Sender().ID)
			return c.Send("Спасибо! Ваша регистрация принята. Ожидайте подтверждения администратора.")
		}
	}

	// Обычная обработка сообщений
	user, exists := b.storage.GetUser(c.Sender().ID)
	if !exists {
		return c.Send("Пожалуйста, используйте /start для начала работы с ботом.")
	}

	if !user.Approved {
		return c.Send("Ваш аккаунт еще не подтвержден администратором.")
	}

	// Проверяем, есть ли действие администратора
	if adminAction, ok := b.adminActions[c.Sender().ID]; ok && adminAction.Stage == "waiting_input" {
		if time.Since(adminAction.StartTime) > 5*time.Minute {
			delete(b.adminActions, c.Sender().ID)
			return c.Send("Время ожидания истекло. Пожалуйста, начните сначала.")
		}

		input := strings.TrimSpace(c.Text())
		var targetID int64
		if strings.HasPrefix(input, "@") {
			username := strings.TrimPrefix(input, "@")
			targetID = b.storage.GetUserIDByUsername(username)
			if targetID == 0 {
				return c.Send("Пользователь с таким username не найден.")
			}
		} else {
			var err error
			targetID, err = strconv.ParseInt(input, 10, 64)
			if err != nil {
				return c.Send("Неверный формат ID пользователя.")
			}
		}

		targetUser, exists := b.storage.GetUser(targetID)
		if !exists {
			return c.Send("Пользователь не найден.")
		}

		switch adminAction.Action {
		case "promote":
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
			delete(b.adminActions, c.Sender().ID)
			return c.Send(fmt.Sprintf("Пользователь %s %s назначен администратором.",
				targetUser.FirstName, targetUser.LastName))

		case "demote":
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
			delete(b.adminActions, c.Sender().ID)
			return c.Send(fmt.Sprintf("С пользователя %s %s сняты права администратора.",
				targetUser.FirstName, targetUser.LastName))
		}
	}

	// Проверяем, находится ли пользователь в процессе создания задачи
	if _, ok := b.taskCreationStates[c.Sender().ID]; ok {
		return b.handleTaskText(c)
	}

	return nil
}

// Start запускает обработчики бота и фоновую обработку уведомлений.
func (b *Bot) Start() {
	// Запускаем обработку уведомлений
	go func() {
		for msg := range b.notifications {
			b.SendNotification(msg)
		}
	}()

	go b.bot.Start()
}

// startFullScan запускает фоновый fullscan, который проверяет пронумерованные ключи ITS-<n>.
// rng — количество последовательных номеров для проверки за запуск.
func (b *Bot) startFullScan(rng int) error {
	b.fullScanMu.Lock()
	defer b.fullScanMu.Unlock()
	if b.fullScanRunning {
		return fmt.Errorf("fullscan уже запущен")
	}
	ctx, cancel := context.WithCancel(context.Background())
	b.fullScanCancel = cancel
	b.fullScanRunning = true
	go b.fullScanLoop(ctx, rng)
	return nil
}

// stopFullScan останавливает запущенный fullscan.
func (b *Bot) stopFullScan() error {
	b.fullScanMu.Lock()
	defer b.fullScanMu.Unlock()
	if !b.fullScanRunning {
		return fmt.Errorf("fullscan не запущен")
	}
	if b.fullScanCancel != nil {
		b.fullScanCancel()
	}
	b.fullScanRunning = false
	return nil
}

// fullScanLoop выполняет последовательные вызовы GetTaskByID для ITS-N с динамическим throttle.
// Поведение: пока находятся новые задачи — 1 запрос в секунду; если в течение 1 минуты новых задач нет — 1 запрос в минуту.
func (b *Bot) fullScanLoop(ctx context.Context, rng int) {
	defer func() {
		b.fullScanMu.Lock()
		b.fullScanRunning = false
		b.fullScanMu.Unlock()
	}()

	last := b.storage.GetLastScanned()
	if last < 0 {
		last = 0
	}

	idleSince := time.Time{}
	throttleShort := time.Second
	throttleLong := time.Minute
	currentThrottle := throttleShort

	for i := 1; i <= rng; i++ {
		select {
		case <-ctx.Done():
			log.Printf("fullScanLoop: cancelled")
			return
		default:
		}

		n := last + i
		key := fmt.Sprintf("ITS-%d", n)
		task, err := b.yougileClient.GetTaskByID(key)
		if err == nil && task != nil {
			// found
			tkey := key
			if task.Key != "" {
				tkey = task.Key
			} else if task.ExternalID != "" {
				tkey = task.ExternalID
			}
			known := b.storage.IsKnownKey(tkey)
			log.Printf("fullScanLoop: found task by key=%s id=%d external=%s title=%q done=%v known=%v", tkey, task.ID, task.ExternalID, task.Title, task.Done, known)
			if !known {
				b.storage.AddKnownKey(tkey)
				if task.ID != 0 {
					b.storage.AddKnownTask(task.ID)
				}
				if !task.Done {
					// Log chat targets
					chats := b.storage.GetChatIDs()
					log.Printf("fullScanLoop: sending notification for task %s to %d chats", tkey, len(chats))
					b.SendNotification(b.formatTaskNotification(*task))
					log.Printf("fullScanLoop: SendNotification called for task %s", tkey)
				}
			}
			b.storage.SetLastScanned(n)
			idleSince = time.Time{} // reset idle timer
			currentThrottle = throttleShort
		} else {
			// not found or error
			if idleSince.IsZero() {
				idleSince = time.Now()
			}
			if time.Since(idleSince) > time.Minute {
				currentThrottle = throttleLong
			}
		}

		// sleep respecting the current throttle but exit early on cancel
		sleepUntil := time.Now().Add(currentThrottle)
		for time.Now().Before(sleepUntil) {
			select {
			case <-ctx.Done():
				log.Printf("fullScanLoop: cancelled during sleep")
				return
			case <-time.After(200 * time.Millisecond):
			}
		}
	}
}

// Stop корректно завершает работу бота и закрывает канал уведомлений.
func (b *Bot) Stop() {
	close(b.notifications)
	b.bot.Stop()
}

// SendNotification отправляет указанное сообщение во все чаты, зарегистрированные в хранилище.
func (b *Bot) SendNotification(msg string) {
	chats := b.storage.GetChatIDs()
	if len(chats) == 0 {
		log.Printf("SendNotification: пропускаем отправку — нет зарегистрированных chat_ids")
		return
	}
	for _, chatID := range chats {
		if _, err := b.bot.Send(&telebot.Chat{ID: chatID}, msg); err != nil {
			log.Printf("Ошибка отправки уведомления в чат %d: %v", chatID, err)
		}
	}
}

// showPendingRequests формирует и отправляет список ожидающих подтверждения запросов администратора.
func (b *Bot) showPendingRequests(c telebot.Context) error {
	var menu *telebot.ReplyMarkup
	var buttons []telebot.Btn
	var msg string

	for _, req := range b.pendingReqs {
		user, exists := b.storage.GetUser(req.UserID)
		if !exists {
			continue
		}

		var reqType string
		switch req.Type {
		case "registration":
			reqType = "регистрация"
		case "address_change":
			reqType = "изменение адреса"
		}

		msg += fmt.Sprintf("\n👤 %s %s (%d)\n📝 Тип: %s\n💼 Должность: %s\n",
			user.FirstName, user.LastName, user.TelegramID, reqType, user.Position)

		if req.Type == "address_change" {
			msg += fmt.Sprintf("🏢 Адрес: %s\n🚪 Кабинет: %s\n",
				req.BuildingAddress, req.RoomNumber)
		}

		// Создаем кнопки для каждого запроса
		menu = &telebot.ReplyMarkup{}
		btnApprove := menu.Data("✅ Подтвердить "+strconv.FormatInt(req.UserID, 10), "approve|"+strconv.FormatInt(req.UserID, 10))
		btnReject := menu.Data("❌ Отклонить "+strconv.FormatInt(req.UserID, 10), "reject|"+strconv.FormatInt(req.UserID, 10))
		buttons = append(buttons, btnApprove, btnReject)
	}

	if len(msg) == 0 {
		return c.Send("Нет запросов, ожидающих подтверждения.")
	}

	// Добавляем кнопки к сообщению
	menu.Inline(
		menu.Row(buttons...),
	)

	return c.Send("📋 Запросы на подтверждение:"+msg, menu)
}

// handleApprove обрабатывает подтверждение регистрации или изменения адреса
func (b *Bot) handleApprove(c telebot.Context) error {
	log.Printf("handleApprove invoked by user=%d callback=%v", c.Sender().ID, func() string {
		if c.Callback() != nil {
			return c.Callback().Data
		}
		return "<nil>"
	}())
	admin, exists := b.storage.GetUser(c.Sender().ID)
	if !exists || admin.Role != models.RoleAdmin {
		return c.Send("У вас нет прав для выполнения этой команды.")
	}

	// Проверяем наличие данных в callback
	if c.Callback() != nil && c.Callback().Data != "" {
		// Разбираем данные из callback
		parts := strings.Split(c.Callback().Data, "|")
		if len(parts) != 2 || parts[0] != "approve" {
			return c.Send("Некорректные данные запроса.")
		}

		userID, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return c.Send("Ошибка при обработке запроса.")
		}

		req, exists := b.pendingReqs[userID]
		if !exists {
			return c.Send("Запрос не найден.")
		}

		user, exists := b.storage.GetUser(userID)
		if !exists {
			return c.Send("Пользователь не найден.")
		}

		switch req.Type {
		case "registration":
			user.Approved = true
		case "address_change":
			user.BuildingAddress = req.BuildingAddress
			user.RoomNumber = req.RoomNumber
			user.AddressChange = false
		}

		b.storage.UpdateUser(user)
		if err := b.storage.SaveData(); err != nil {
			log.Printf("Ошибка сохранения данных после подтверждения: %v", err)
		}
		delete(b.pendingReqs, userID)

		// Уведомляем пользователя
		var userMsg string
		if req.Type == "registration" {
			userMsg = "Ваша регистрация подтверждена! Теперь вы можете использовать бота."
		} else {
			userMsg = "Ваш новый адрес подтвержден."
		}
		msg := userMsg
		if _, err := b.bot.Send(&telebot.User{ID: userID}, msg); err != nil {
			log.Printf("Ошибка отправки уведомления пользователю %d: %v", userID, err)
		}
		// Если подтверждена регистрация — показываем основное меню пользователю
		if req.Type == "registration" {
			if _, err := b.bot.Send(&telebot.User{ID: userID}, "Добро пожаловать!", b.menuForUserID(userID)); err != nil {
				log.Printf("Ошибка отправки mainMenu пользователю %d: %v", userID, err)
			}
		}

		return c.Send("Запрос подтвержден.")
	}

	// Показываем список запросов
	return b.showPendingRequests(c)
}

// handleReject обрабатывает отклонение регистрации или изменения адреса
func (b *Bot) handleReject(c telebot.Context) error {
	log.Printf("handleReject invoked by user=%d callback=%v", c.Sender().ID, func() string {
		if c.Callback() != nil {
			return c.Callback().Data
		}
		return "<nil>"
	}())
	admin, exists := b.storage.GetUser(c.Sender().ID)
	if !exists || admin.Role != models.RoleAdmin {
		return c.Send("У вас нет прав для выполнения этой команды.")
	}

	// Проверяем наличие данных в callback
	if c.Callback() != nil && c.Callback().Data != "" {
		// Разбираем данные из callback
		parts := strings.Split(c.Callback().Data, "|")
		if len(parts) != 2 || parts[0] != "reject" {
			return c.Send("Некорректные данные запроса.")
		}

		userID, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return c.Send("Ошибка при обработке запроса.")
		}

		req, exists := b.pendingReqs[userID]
		if !exists {
			return c.Send("Запрос не найден.")
		}

		user, exists := b.storage.GetUser(userID)
		if !exists {
			return c.Send("Пользователь не найден.")
		}

		switch req.Type {
		case "registration":
			// Удаляем пользователя из хранилища
			delete(b.storage.GetUsers(), userID)
		case "address_change":
			user.AddressChange = false
			b.storage.UpdateUser(user)
		}

		// Если это регистрация, удаляем пользователя из storage корректно
		if req.Type == "registration" {
			b.storage.DeleteUser(userID)
			if err := b.storage.SaveData(); err != nil {
				log.Printf("Ошибка сохранения данных после отклонения: %v", err)
			}
		}
		delete(b.pendingReqs, userID)

		// Уведомляем пользователя
		var userMsg string
		if req.Type == "registration" {
			userMsg = "Ваша регистрация отклонена. Пожалуйста, свяжитесь с администратором."
		} else {
			userMsg = "Изменение адреса отклонено. Пожалуйста, свяжитесь с администратором."
		}
		if _, err := b.bot.Send(&telebot.User{ID: userID}, userMsg); err != nil {
			log.Printf("Ошибка отправки уведомления пользователю %d: %v", userID, err)
		}

		return c.Send("Запрос отклонен.")
	}

	// Показываем список запросов
	return b.showPendingRequests(c)
}

// NotificationChannel возвращает канал для отправки уведомлений
func (b *Bot) NotificationChannel() chan<- string {
	return b.notifications
}

// handleDocument обрабатывает отправку документов
func (b *Bot) handleDocument(c telebot.Context) error {
	// Проверяем, что пользователь авторизован
	_, exists := b.storage.GetUser(c.Sender().ID)
	if !exists {
		return c.Send("Пожалуйста, сначала зарегистрируйтесь и дождитесь подтверждения администратора.")
	}

	if _, ok := b.commentStates[c.Sender().ID]; ok {
		delete(b.commentStates, c.Sender().ID)
		return c.Send("⚠️ К задачам можно прикреплять только изображения. Если вам нужно передать другие файлы, " +
			"пожалуйста, обратитесь к системному администратору напрямую.")
	}

	return c.Send("Пожалуйста, сначала выберите задачу для комментирования.")
}
