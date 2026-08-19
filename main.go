// Программа запускает Telegram-бота и фоновые службы для интеграции с Yougile.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"yougile_bot4/internal/api"
	"yougile_bot4/internal/bot"
	"yougile_bot4/internal/logger"
	"yougile_bot4/internal/metrics"
	"yougile_bot4/internal/models"
	"yougile_bot4/internal/storage"

	"github.com/joho/godotenv"
)

// defaultScanRange используется при поиске пронумерованных ключей задач
var defaultScanRange = 20

// defaultScanKeyPrefix — префикс коротких номерных ключей задач (idTaskProject в Yougile),
// например "ID" даёт ключи вида "ID-2990". Префикс специфичен для конкретного проекта/доски
// в Yougile и переопределяется через YOUGILE_KEY_PREFIX.
var defaultScanKeyPrefix = "ID"

func init() {
	// Загружаем переменные из .env файла
	if err := godotenv.Load(); err != nil {
		// Если файл .env не найден, используем переменные окружения системы
		log.Printf("Файл .env не найден, используем переменные окружения системы")
	}
}

// main является точкой входа в приложение
// Выполняет следующие шаги:
// 1. Проверяет наличие необходимых переменных окружения
// 2. Инициализирует конфигурацию
// 3. Настраивает логирование
// 4. Создает хранилище данных
// 5. Инициализирует клиент Yougile
// 6. Создает и запускает Telegram бота
// 7. Запускает фоновые процессы (сохранение данных, проверка задач)
// 8. Ожидает сигнал завершения для graceful shutdown
func main() {
	// Создаем контекст с поддержкой отмены
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Проверяем наличие всех необходимых переменных окружения
	requiredEnvVars := []string{"YOUGILE_TOKEN", "YOUGILE_BOARD", "TELEGRAM_TOKEN"}
	for _, envVar := range requiredEnvVars {
		if os.Getenv(envVar) == "" {
			log.Fatalf("Отсутствует обязательная переменная окружения: %s", envVar)
		}
	}

	// Инициализируем метрики
	metrics := metrics.NewMetrics()

	// Инициализация конфигурации приложения
	config := &models.Config{
		// Токены для доступа к API
		YougileToken:  os.Getenv("YOUGILE_TOKEN"),  // Токен Yougile API
		YougileBoard:  os.Getenv("YOUGILE_BOARD"),  // ID доски в Yougile
		TelegramToken: os.Getenv("TELEGRAM_TOKEN"), // Токен Telegram бота

		// Пути к файлам для хранения данных
		KnownTasksFile: "data/known_tasks.json",    // Список известных задач
		ChatIDsFile:    "data/chat_ids.json",       // Список чатов для уведомлений
		UsersFile:      "data/users.json",          // Данные пользователей
		TasksFile:      "data/tasks.json",          // Кэш задач
		TemplatesFile:  "data/task_templates.json", // Шаблоны задач
		LogFile:        "logs/bot.log",             // Файл логов

		// Настройки логирования
		MaxLogSize: 10 * 1024 * 1024,    // Максимальный размер лог-файла: 10 MB
		MaxLogAge:  30 * 24 * time.Hour, // Время хранения логов: 30 дней

		// Настройки работы бота
		TasksLimit:      100,              // Максимальное количество задач для получения
		CheckInterval:   1 * time.Minute,  // Интервал проверки новых задач
		SaveInterval:    5 * time.Minute,  // Интервал сохранения данных
		MinMsgLen:       10,               // Минимальная длина сообщения
		RegTimeout:      24 * time.Hour,   // Таймаут регистрации
		HTTPTimeout:     30 * time.Second, // Таймаут HTTP запросов
		GracefulTimeout: 30 * time.Second, // Таймаут graceful shutdown
		// retry defaults (можно переопределить через переменные окружения)
		RetryCount:      3,
		RetryWait:       500 * time.Millisecond,
		MaxRetryElapsed: 10 * time.Second,
	}

	// Настройка логирования
	logWriter, err := logger.GetWriter(config.LogFile, config.MaxLogSize, config.MaxLogAge)
	if err != nil {
		log.Fatalf("Ошибка создания писателя логов: %v", err)
	}
	log.SetOutput(logWriter)

	// Инициализация хранилища
	store, err := storage.NewStorage(
		config.KnownTasksFile,
		config.ChatIDsFile,
		config.UsersFile,
		config.TasksFile,
		config.TemplatesFile,
		metrics,
	)
	if err != nil {
		log.Fatalf("Ошибка инициализации хранилища: %v", err)
	}

	// Создание клиента Yougile
	yougileClient := api.NewClient(
		config.YougileToken,
		config.YougileBoard,
		config.HTTPTimeout,
		metrics,
	)

	// Override retry policy from environment if provided
	if rc := os.Getenv("YOUGILE_RETRY_COUNT"); rc != "" {
		if v, err := strconv.Atoi(rc); err == nil && v > 0 {
			config.RetryCount = v
		}
	}
	if rw := os.Getenv("YOUGILE_RETRY_WAIT_MS"); rw != "" {
		if v, err := strconv.Atoi(rw); err == nil && v > 0 {
			config.RetryWait = time.Duration(v) * time.Millisecond
		}
	}
	if rm := os.Getenv("YOUGILE_MAX_RETRY_ELAPSED_SEC"); rm != "" {
		if v, err := strconv.Atoi(rm); err == nil && v > 0 {
			config.MaxRetryElapsed = time.Duration(v) * time.Second
		}
	}

	yougileClient.SetRetryPolicy(config.RetryCount, config.RetryWait, config.MaxRetryElapsed)

	// If COLUMN_ID is provided in env, pass it to client so GetTasks can filter by column
	if col := os.Getenv("COLUMN_ID"); col != "" {
		yougileClient.SetColumnID(col)
	}

	// scan range for numeric task keys (default 20)
	if sr := os.Getenv("YOUGILE_SCAN_RANGE"); sr != "" {
		if v, err := strconv.Atoi(sr); err == nil && v > 0 {
			defaultScanRange = v
		}
	}
	if kp := os.Getenv("YOUGILE_KEY_PREFIX"); kp != "" {
		defaultScanKeyPrefix = kp
	}

	// Создание и запуск бота
	boardID := config.YougileBoard
	if boardID == "" {
		log.Fatalf("Неверный формат ID доски: пустой YOUGILE_BOARD")
	}

	telegramBot, err := bot.NewBot(
		config.TelegramToken,
		store,
		yougileClient,
		boardID,
		config.RegTimeout,
		config.MinMsgLen,
		metrics,
	)
	if err != nil {
		log.Fatalf("Ошибка создания бота: %v", err)
	}

	telegramBot.Start()

	// Периодическое сохранение данных
	saveTicker := time.NewTicker(config.SaveInterval)
	go func() {
		defer saveTicker.Stop()
		for {
			select {
			case <-saveTicker.C:
				if err := store.SaveData(); err != nil {
					log.Printf("Ошибка сохранения данных: %v", err)
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// Проверка новых задач
	checkTicker := time.NewTicker(config.CheckInterval)
	go func() {
		defer checkTicker.Stop()

		// Выполняем первую проверку сразу
		checkNewTasks(ctx, yougileClient, store, telegramBot, config.TasksLimit)

		for {
			select {
			case <-checkTicker.C:
				checkNewTasks(ctx, yougileClient, store, telegramBot, config.TasksLimit)
			case <-ctx.Done():
				return
			}
		}
	}()

	// Настройка graceful shutdown:
	// - Создаем канал для получения сигналов ОС
	// - Подписываемся на SIGINT (Ctrl+C) и SIGTERM (kill)
	// - Ожидаем получения сигнала
	// - При получении сигнала выполняем корректное завершение работы
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	// При получении сигнала завершения:
	// 1. Логируем начало процесса завершения
	// 2. Отменяем контекст для остановки фоновых процессов
	// 3. Останавливаем все сервисы
	// 4. Сохраняем данные перед выходом
	log.Println("Получен сигнал завершения, останавливаем работу...")

	// Отменяем контекст
	cancel()

	// Создаем таймер для graceful shutdown
	shutdownTimer := time.NewTimer(config.GracefulTimeout)
	defer shutdownTimer.Stop()

	// Канал для ожидания завершения сохранения
	done := make(chan bool)

	go func() {
		if err := store.SaveData(); err != nil {
			log.Printf("Ошибка сохранения данных при завершении: %v", err)
		}
		done <- true
	}()

	// Ждем либо завершения сохранения, либо таймаута
	select {
	case <-done:
		log.Println("Данные успешно сохранены")
	case <-shutdownTimer.C:
		log.Println("Превышено время graceful shutdown")
	}

	telegramBot.Stop()
}

// checkNewTasks проверяет новые задачи на доске Yougile и отправляет уведомления
// - получает список задач через API
// - проверяет, не было ли уже уведомления о задаче
// - отправляет уведомление только о новых незавершенных задачах
// - сохраняет ID задачи в списке известных
func checkNewTasks(ctx context.Context, client *api.Client, store *storage.Storage, bot *bot.Bot, limit int) {
	// Получаем список последних задач с ограничением по количеству
	select {
	case <-ctx.Done():
		return
	default:
		tasks, err := client.GetTasks(limit)
		if err != nil {
			log.Printf("Ошибка получения задач: %v", err)
			return
		}

		colEnv := os.Getenv("COLUMN_ID")
		log.Printf("checkNewTasks: %d задач получено", len(tasks))

		// Fallback: if column-scoped fetch returned no tasks but COLUMN_ID is set,
		// try fetching without column filter (some Yougile instances don't return tasks
		// for column-scoped queries reliably).
		if len(tasks) == 0 && colEnv != "" {
			// temporarily clear columnId on client
			client.SetColumnID("")
			broader, berr := client.GetTasks(limit)
			// restore column ID from env for future runs
			client.SetColumnID(colEnv)
			if berr == nil {
				log.Printf("checkNewTasks: fallback %d задач", len(broader))
				tasks = broader
			}
			// If still empty, trigger a numeric ITS scan in background to discover manual tasks
			if len(broader) == 0 {
				go scanNumericKeys(client, store, bot, defaultScanRange)
			}
		}

		// Проверяем каждую задачу
		newCount := 0
		notifyCount := 0
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
			known := store.IsKnownKey(key)
			if !known {
				store.AddKnownKey(key)
				if task.ID != 0 {
					store.AddKnownTask(task.ID)
				}
				newCount++
				if !task.Done {
					bot.SendNotification(models.FormatTaskNotification(task))
					notifyCount++
				}
			}
		}
		if newCount > 0 {
			log.Printf("checkNewTasks: новых задач %d, уведомлений %d", newCount, notifyCount)
		}
	}
}

// scanNumericKeys выполняет быстрый пробег по пронумерованным коротким ключам вида
// "<defaultScanKeyPrefix>-N" (например "ID-2990"), начиная с последнего сохранённого
// в хранилище значения. Находит задачи через GetTaskByID и уведомляет админов при
// обнаружении новых.
func scanNumericKeys(client *api.Client, store *storage.Storage, bot *bot.Bot, rng int) {
	last := store.GetLastScanned()
	if last < 0 {
		last = 0
	}
	maxScan := last + rng
	log.Printf("scanNumericKeys: сканирование %s-%d..%s-%d", defaultScanKeyPrefix, last+1, defaultScanKeyPrefix, maxScan)
	found := 0
	notified := 0
	for n := last + 1; n <= maxScan; n++ {
		key := fmt.Sprintf("%s-%d", defaultScanKeyPrefix, n)
		t, err := client.GetTaskByIDQuiet(key)
		if err != nil || t == nil {
			continue
		}
		taskKey := key
		if t.Key != "" {
			taskKey = t.Key
		} else if t.ExternalID != "" {
			taskKey = t.ExternalID
		}
		known := store.IsKnownKey(taskKey)
		if !known {
			store.AddKnownKey(taskKey)
			if t.ID != 0 {
				store.AddKnownTask(t.ID)
			}
			found++
			if !t.Done {
				bot.SendNotification(models.FormatTaskNotification(*t))
				notified++
			}
		}
		store.SetLastScanned(n)
	}
	if found > 0 {
		log.Printf("scanNumericKeys: найдено новых задач %d, уведомлений %d", found, notified)
	}
}
