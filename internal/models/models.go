// Package models содержит структуры данных, используемые в проекте:
// задачи, пользователи, комментарии и конфигурация.
package models

import "time"

// TaskStatus представляет статус задачи.
// Возможные значения определены константами ниже (TaskStatusNew, TaskStatusInWork и т.д.).
type TaskStatus string

const (
	// TaskStatusNew означает, что задача только что создана и ещё не назначена.
	TaskStatusNew TaskStatus = "new"
	// TaskStatusInWork означает, что над задачей ведётся работа.
	TaskStatusInWork TaskStatus = "in_work"
	// TaskStatusDone означает, что задача выполнена.
	TaskStatusDone TaskStatus = "done"
	// TaskStatusCancelled означает, что задача отменена.
	TaskStatusCancelled TaskStatus = "cancelled"
)

// AttachmentType представляет тип вложения (изображение, файл и т.д.).
type AttachmentType string

const (
	// AttachmentTypeImage — вложение является изображением.
	AttachmentTypeImage AttachmentType = "image"
	// AttachmentTypeFile — вложение является файлом произвольного типа.
	AttachmentTypeFile AttachmentType = "file"
)

// Attachment представляет вложение в комментарии или задаче.
// Поля описывают метаданные вложения и ссылку на содержимое.
type Attachment struct {
	ID        string         `json:"id"`
	Type      AttachmentType `json:"type"`
	URL       string         `json:"url"`
	CreatedAt time.Time      `json:"created_at"`
	FileID    string         `json:"file_id,omitempty"` // Telegram File ID
}

// Comment представляет комментарий к задаче.
// Содержит автора, текст и возможные вложения.
type Comment struct {
	ID          int64        `json:"id"`
	TaskID      int64        `json:"task_id"`
	AuthorID    string       `json:"author_id"`
	Text        string       `json:"text"`
	Attachments []Attachment `json:"attachments,omitempty"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
}

// Task представляет задачу в системе Yougile.
// Включает поля метаданных, статуса, оценок и комментариев.
type Task struct {
	ID          int64      `json:"id"`
	BoardID     int64      `json:"board_id"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Status      TaskStatus `json:"status"`
	Done        bool       `json:"done"`
	ParentID    int64      `json:"parent_id,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	DueDate     time.Time  `json:"due_date,omitempty"`
	Priority    int        `json:"priority"`
	Assignee    string     `json:"assignee,omitempty"`
	Labels      []string   `json:"labels,omitempty"`
	Estimate    float64    `json:"estimate,omitempty"`   // оценка в часах
	TimeSpent   float64    `json:"time_spent,omitempty"` // затраченное время в часах
	Comments    []Comment  `json:"comments,omitempty"`
	Attachments []string   `json:"attachments,omitempty"` // URLs вложений
}

// UserRole определяет роль пользователя в системе (админ или пользователь).
type UserRole string

const (
	// RoleAdmin — роль администратора.
	RoleAdmin UserRole = "admin"
	// RoleUser — роль обычного пользователя.
	RoleUser UserRole = "user"
)

// User представляет пользователя бота и связанные с ним данные (телеграм, роль, адрес и т.д.).
type User struct {
	TelegramID      int64    `json:"telegram_id"`
	Username        string   `json:"username"` // Username в Telegram
	FirstName       string   `json:"first_name"`
	LastName        string   `json:"last_name"`
	Position        string   `json:"position"`
	BuildingAddress string   `json:"building_address"`
	RoomNumber      string   `json:"room_number"`
	Address         string   `json:"address,omitempty"` // Для обратной совместимости
	Role            UserRole `json:"role"`
	Approved        bool     `json:"approved"`
	AddressChange   bool     `json:"address_change"` // Ожидает подтверждения изменения адреса
}

// PendingRequest представляет запрос пользователя, требующий подтверждения администратора.
type PendingRequest struct {
	UserID          int64     `json:"user_id"`
	Type            string    `json:"type"` // "registration" или "address_change"
	BuildingAddress string    `json:"building_address,omitempty"`
	RoomNumber      string    `json:"room_number,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

// FAQItem представляет элемент FAQ (вопрос-ответ).
type FAQItem struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// FAQData представляет набор элементов FAQ, индексированных по ключу.
type FAQData map[string]FAQItem

// Config содержит конфигурацию приложения, получаемую из окружения.
// Включает параметры Yougile, Telegram и файлы хранения.
type Config struct {
	YougileToken    string
	YougileBoard    string
	TelegramToken   string
	KnownTasksFile  string
	ChatIDsFile     string
	UsersFile       string
	TemplatesFile   string
	LogFile         string
	MaxLogSize      int64
	MaxLogAge       time.Duration
	TasksLimit      int
	TasksFile       string
	CheckInterval   time.Duration
	SaveInterval    time.Duration
	MinMsgLen       int
	RegTimeout      time.Duration
	HTTPTimeout     time.Duration
	GracefulTimeout time.Duration
	// Retry policy for Yougile client
	RetryCount      int
	RetryWait       time.Duration
	MaxRetryElapsed time.Duration
}
