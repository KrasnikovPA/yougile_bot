// Package storage реализует файл-ориентированное хранилище данных приложения.
package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"yougile_bot4/internal/metrics"
	"yougile_bot4/internal/models"
)

// Storage представляет файл-ориентированное хранилище данных приложения.
// Используется для чтения/записи пользователей, задач, шаблонов и FAQ.
type Storage struct {
	knownTasks      map[string]bool
	chatIDs         []int64
	users           map[int64]*models.User
	usersByUsername map[string]int64     // Добавляем индекс для поиска по username
	faq             models.FAQData       // FAQ данные
	taskTemplates   models.TaskTemplates // Шаблоны задач
	tasks           []*models.Task
	mu              sync.RWMutex
	isDirty         bool // Флаг изменения данных

	knownTasksFile string
	chatIDsFile    string
	usersFile      string
	tasksFile      string
	templatesFile  string

	metrics *metrics.Metrics // Метрики хранилища
}

// NewStorage создает новое хранилище и загружает данные из указанных файлов.
// knownTasksFile, chatIDsFile, usersFile, tasksFile, templatesFile — пути к JSON файлам.
func NewStorage(knownTasksFile, chatIDsFile, usersFile, tasksFile, templatesFile string, m *metrics.Metrics) (*Storage, error) {
	s := &Storage{
		knownTasks:      make(map[string]bool),
		chatIDs:         make([]int64, 0),
		users:           make(map[int64]*models.User),
		usersByUsername: make(map[string]int64),
		tasks:           make([]*models.Task, 0),
		knownTasksFile:  knownTasksFile,
		tasksFile:       tasksFile,
		chatIDsFile:     chatIDsFile,
		usersFile:       usersFile,
		templatesFile:   templatesFile,
		metrics:         m,
	}

	if err := s.loadData(); err != nil {
		return nil, err
	}

	return s, nil
}

// loadData загружает данные из файлов
func (s *Storage) loadData() error {
	if err := s.loadJSON(s.knownTasksFile, &s.knownTasks); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := s.loadJSON(s.chatIDsFile, &s.chatIDs); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := s.loadJSON(s.usersFile, &s.users); err != nil && !os.IsNotExist(err) {
		return err
	}
	// Восстановим индекс usersByUsername
	s.usersByUsername = make(map[string]int64, len(s.users))
	for id, u := range s.users {
		if u != nil && u.Username != "" {
			s.usersByUsername[u.Username] = id
		}
	}
	if err := s.LoadFAQ(); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := s.LoadTaskTemplates(); err != nil && !os.IsNotExist(err) {
		return err
	}
	// Загрузим задачи из файла tasksFile
	if err := s.loadJSON(s.tasksFile, &s.tasks); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// SaveData сохраняет текущие данные в файлы, если есть изменения.
// Осуществляет атомарную запись через временные файлы.
func (s *Storage) SaveData() error {
	s.mu.RLock()
	if !s.isDirty {
		s.mu.RUnlock()
		return nil
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()

	start := time.Now()

	if err := s.saveJSON(s.knownTasksFile, s.knownTasks); err != nil {
		if s.metrics != nil {
			s.metrics.IncAPIErrors()
		}
		return err
	}
	if err := s.saveJSON(s.chatIDsFile, s.chatIDs); err != nil {
		if s.metrics != nil {
			s.metrics.IncAPIErrors()
		}
		return err
	}
	if err := s.saveJSON(s.usersFile, s.users); err != nil {
		if s.metrics != nil {
			s.metrics.IncAPIErrors()
		}
		return err
	}

	// Сохраним задачи
	if err := s.saveJSON(s.tasksFile, s.tasks); err != nil {
		if s.metrics != nil {
			s.metrics.IncAPIErrors()
		}
		return err
	}

	// Сохраняем шаблоны напрямую (чтобы избежать повторной блокировки s.mu внутри SaveTaskTemplates)
	if err := s.saveJSON(s.templatesFile, s.taskTemplates); err != nil {
		if s.metrics != nil {
			s.metrics.IncAPIErrors()
		}
		return err
	}

	s.isDirty = false
	if s.metrics != nil {
		s.metrics.UpdateLatency(time.Since(start))
	}
	return nil
}

// loadJSON загружает данные из JSON файла
func (s *Storage) loadJSON(filename string, v interface{}) error {
	data, err := os.ReadFile(filename)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// saveJSON сохраняет данные в JSON файл
func (s *Storage) saveJSON(filename string, v interface{}) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}

	dir := filepath.Dir(filename)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	// Записываем в временный файл и затем переименовываем — атомарная запись
	tmp := filename + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, filename)
}

// AddKnownTask добавляет идентификатор задачи в набор известных задач.
func (s *Storage) AddKnownTask(taskID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.knownTasks[fmt.Sprintf("%d", taskID)] = true
	s.isDirty = true
}

// IsKnownTask возвращает true, если задача уже была увидена ранее.
func (s *Storage) IsKnownTask(taskID int64) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.knownTasks[fmt.Sprintf("%d", taskID)]
}

// AddChatID добавляет идентификатор чата, в который будут отправляться уведомления.
func (s *Storage) AddChatID(chatID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range s.chatIDs {
		if id == chatID {
			return
		}
	}
	s.chatIDs = append(s.chatIDs, chatID)
	s.isDirty = true
}

// GetChatIDs возвращает копию списка идентификаторов чатов.
func (s *Storage) GetChatIDs() []int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]int64, len(s.chatIDs))
	copy(result, s.chatIDs)
	return result
}

// AddUser добавляет пользователя
func (s *Storage) AddUser(user *models.User) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Если это первый пользователь, делаем его администратором
	if len(s.users) == 0 {
		user.Role = models.RoleAdmin
		user.Approved = true // Автоматически подтверждаем первого пользователя
	}

	s.users[user.TelegramID] = user
	if user.Username != "" {
		s.usersByUsername[user.Username] = user.TelegramID
	}
	s.isDirty = true
}

// GetUser возвращает пользователя по Telegram ID и флаг, найден ли он.
func (s *Storage) GetUser(telegramID int64) (*models.User, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	user, ok := s.users[telegramID]
	return user, ok
}

// GetAllUsers возвращает срез всех пользователей, сохранённых в хранилище.
func (s *Storage) GetAllUsers() []*models.User {
	s.mu.RLock()
	defer s.mu.RUnlock()

	users := make([]*models.User, 0, len(s.users))
	for _, user := range s.users {
		users = append(users, user)
	}
	return users
}

// UpdateUser обновляет или создает запись пользователя и поддерживает индекс username.
func (s *Storage) UpdateUser(user *models.User) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Если пользователь уже существует, удаляем его старый username
	if oldUser, ok := s.users[user.TelegramID]; ok && oldUser.Username != "" {
		delete(s.usersByUsername, oldUser.Username)
	}

	// Обновляем пользователя
	s.users[user.TelegramID] = user

	// Добавляем новый username в индекс
	if user.Username != "" {
		s.usersByUsername[user.Username] = user.TelegramID
	}
	s.isDirty = true
}

// GetUsers возвращает копию внутренней карты пользователей.
func (s *Storage) GetUsers() map[int64]*models.User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[int64]*models.User, len(s.users))
	for k, v := range s.users {
		result[k] = v
	}
	return result
}

// AddTask добавляет новую задачу в хранилище.
func (s *Storage) AddTask(task *models.Task) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks = append(s.tasks, task)
	s.isDirty = true
}

// GetTasks возвращает копию среза всех задач.
func (s *Storage) GetTasks() []*models.Task {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*models.Task, len(s.tasks))
	copy(result, s.tasks)
	return result
}

// UpdateTask обновляет существующую задачу по ID (заменяет запись).
func (s *Storage) UpdateTask(task *models.Task) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, t := range s.tasks {
		if t.ID == task.ID {
			s.tasks[i] = task
			break
		}
	}
	s.isDirty = true
}
