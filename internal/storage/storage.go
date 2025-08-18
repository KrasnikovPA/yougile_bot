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

// Storage представляет хранилище данных
type Storage struct {
	knownTasks      map[string]bool
	chatIDs         []int64
	users           map[int64]*models.User
	usersByUsername map[string]int64 // Добавляем индекс для поиска по username
	faq             models.FAQData   // FAQ данные
	tasks           []*models.Task
	mu              sync.RWMutex
	isDirty         bool // Флаг изменения данных

	knownTasksFile string
	chatIDsFile    string
	usersFile      string
	tasksFile      string

	metrics *metrics.Metrics // Метрики хранилища
}

// NewStorage создает новое хранилище
func NewStorage(knownTasksFile, chatIDsFile, usersFile, tasksFile string) (*Storage, error) {
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
	if err := s.LoadFAQ(); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// SaveData сохраняет данные в файлы только если были изменения
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
		s.metrics.IncAPIErrors()
		return err
	}
	if err := s.saveJSON(s.chatIDsFile, s.chatIDs); err != nil {
		s.metrics.IncAPIErrors()
		return err
	}
	if err := s.saveJSON(s.usersFile, s.users); err != nil {
		s.metrics.IncAPIErrors()
		return err
	}

	s.isDirty = false
	s.metrics.UpdateLatency(time.Since(start))
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

	return os.WriteFile(filename, data, 0644)
}

// AddKnownTask добавляет задачу в список известных
func (s *Storage) AddKnownTask(taskID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.knownTasks[fmt.Sprintf("%d", taskID)] = true
}

// IsKnownTask проверяет, известна ли задача
func (s *Storage) IsKnownTask(taskID int64) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.knownTasks[fmt.Sprintf("%d", taskID)]
}

// AddChatID добавляет ID чата в список
func (s *Storage) AddChatID(chatID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range s.chatIDs {
		if id == chatID {
			return
		}
	}
	s.chatIDs = append(s.chatIDs, chatID)
}

// GetChatIDs возвращает список ID чатов
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
}

// GetUser возвращает пользователя по ID
func (s *Storage) GetUser(telegramID int64) (*models.User, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	user, ok := s.users[telegramID]
	return user, ok
}

// GetAllUsers возвращает список всех пользователей
func (s *Storage) GetAllUsers() []*models.User {
	s.mu.RLock()
	defer s.mu.RUnlock()

	users := make([]*models.User, 0, len(s.users))
	for _, user := range s.users {
		users = append(users, user)
	}
	return users
}

// UpdateUser обновляет данные пользователя
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
}

// GetUsers возвращает всех пользователей
func (s *Storage) GetUsers() map[int64]*models.User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[int64]*models.User, len(s.users))
	for k, v := range s.users {
		result[k] = v
	}
	return result
}

// AddTask добавляет новую задачу
func (s *Storage) AddTask(task *models.Task) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks = append(s.tasks, task)
}

// GetTasks возвращает все задачи
func (s *Storage) GetTasks() []*models.Task {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*models.Task, len(s.tasks))
	copy(result, s.tasks)
	return result
}

// UpdateTask обновляет существующую задачу
func (s *Storage) UpdateTask(task *models.Task) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, t := range s.tasks {
		if t.ID == task.ID {
			s.tasks[i] = task
			break
		}
	}
}
