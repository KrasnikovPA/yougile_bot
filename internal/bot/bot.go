// Package bot содержит реализацию Telegram-бота и обработчиков команд.
package bot

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"yougile_bot4/internal/api"
	"yougile_bot4/internal/metrics"
	"yougile_bot4/internal/models"
	"yougile_bot4/internal/storage"

	"gopkg.in/telebot.v3"
)

// Кнопки основного меню
var (
	mainMenu   = &telebot.ReplyMarkup{ResizeKeyboard: true}
	btnHelp    = mainMenu.Text("❓ Помощь")
	btnAddress = mainMenu.Text("🏠 Изменить адрес")
	btnNewTask = mainMenu.Text("📝 Новая задача")
	btnFAQ     = mainMenu.Text("ℹ️ Частые вопросы")

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
	}

	// Настраиваем клавиатуру для основного меню
	mainMenu.Reply(
		mainMenu.Row(btnNewTask),
		mainMenu.Row(btnHelp, btnAddress),
		mainMenu.Row(btnFAQ),
	)

	// Настраиваем клавиатуру для администратора
	adminMenu.Reply(
		adminMenu.Row(btnApprove, btnReject),
	)

	bot.setupHandlers()
	return bot, nil
}

// setupHandlers настраивает обработчики команд
func (b *Bot) setupHandlers() {
	// Стандартные команды
	b.bot.Handle("/start", b.handleStart)
	b.bot.Handle("/help", b.handleHelp)
	b.bot.Handle("/address", b.handleChangeAddress)
	// Команда для создания новой задачи через конструктор
	b.bot.Handle("/newtask", b.handleTaskConstructor)

	// Обработчики кнопок
	b.bot.Handle(&btnHelp, b.handleHelp)
	b.bot.Handle(&btnAddress, b.handleChangeAddress)
	b.bot.Handle(&btnApprove, b.handleApprove)
	b.bot.Handle(&btnReject, b.handleReject)
	b.bot.Handle(&btnFAQ, b.handleFAQ)

	// Команды администратора
	b.bot.Handle("/admin", b.handleAdminActions)
	b.bot.Handle("/promote_admin", b.handlePromoteAdmin)
	b.bot.Handle("/demote_admin", b.handleDemoteAdmin)
	b.bot.Handle("/list_users", b.handleListUsers)

	// Обработчики кнопок управления пользователями
	b.bot.Handle(&btnPromoteAdmin, b.handlePromoteAdminButton)
	b.bot.Handle(&btnDemoteAdmin, b.handleDemoteAdminButton)
	b.bot.Handle(&btnEditRole, b.handleEditRole)
	b.bot.Handle(&btnEditAddress, b.handleEditAddress)
	b.bot.Handle(&btnEditName, b.handleEditName)
	b.bot.Handle(&btnBack, b.handleListUsers)

	// Callback-обработчики
	b.bot.Handle(telebot.OnCallback, func(c telebot.Context) error {
		if c.Callback().Unique == "faq" {
			return b.handleFAQCallback(c)
		}

		if strings.HasPrefix(c.Callback().Data, "task_step|") {
			return b.handleTaskStepCallback(c)
		}

		if strings.HasPrefix(c.Callback().Data, "task_select|") {
			return b.handleTaskSelectCallback(c)
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
			return c.Send("Вы уже зарегистрированы и подтверждены в системе.", mainMenu)
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
	user, exists := b.storage.GetUser(c.Sender().ID)
	if !exists {
		return c.Send(`Доступные команды:
/start - Начать работу с ботом
/help - Показать это сообщение`)
	}

	var menu interface{} = mainMenu
	if user.Role == models.RoleAdmin {
		menu = adminMenu
	}

	return c.Send(`Доступные команды:
/start - Начать работу с ботом
/help - Показать это сообщение
/address - Изменить ваш адрес`, menu)
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

// Stop корректно завершает работу бота и закрывает канал уведомлений.
func (b *Bot) Stop() {
	close(b.notifications)
	b.bot.Stop()
}

// SendNotification отправляет указанное сообщение во все чаты, зарегистрированные в хранилище.
func (b *Bot) SendNotification(msg string) {
	for _, chatID := range b.storage.GetChatIDs() {
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

		return c.Send("Запрос подтвержден.")
	}

	// Показываем список запросов
	return b.showPendingRequests(c)
}

// handleReject обрабатывает отклонение регистрации или изменения адреса
func (b *Bot) handleReject(c telebot.Context) error {
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
