package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"sync"
	"time"

	"yougile_bot4/internal/metrics"
	"yougile_bot4/internal/models"
)

var (
	ErrUnauthorized = fmt.Errorf("unauthorized")
	ErrNotFound     = fmt.Errorf("not found")
	ErrRateLimit    = fmt.Errorf("rate limit exceeded")
)

// TaskCache представляет кэш задач
type TaskCache struct {
	Tasks      []models.Task
	UpdatedAt  time.Time
	Expiration time.Duration
}

// Client представляет клиент для работы с API Yougile
type Client struct {
	token      string
	boardID    string
	httpClient *http.Client
	cache      *TaskCache
	mu         sync.RWMutex
	metrics    *metrics.Metrics
	lastCheck  time.Time
}

// NewClient создает новый клиент API
func NewClient(token, boardID string, timeout time.Duration, m *metrics.Metrics) *Client {
	return &Client{
		token:   token,
		boardID: boardID,
		httpClient: &http.Client{
			Timeout: timeout,
		},
		cache: &TaskCache{
			Expiration: 5 * time.Minute,
		},
		metrics: m,
	}
}

// GetTasks получает список задач с доски
func (c *Client) GetTasks(limit int) ([]models.Task, error) {
	start := time.Now()
	c.metrics.IncAPIRequests()
	defer func() {
		c.metrics.UpdateLatency(time.Since(start))
	}()

	c.mu.RLock()
	if c.cache != nil && len(c.cache.Tasks) > 0 && time.Since(c.cache.UpdatedAt) < c.cache.Expiration {
		tasks := make([]models.Task, len(c.cache.Tasks))
		copy(tasks, c.cache.Tasks)
		c.mu.RUnlock()
		return tasks, nil
	}
	c.mu.RUnlock()

	url := fmt.Sprintf("https://yougile.com/api-v2/board/%s/tasks?limit=%d", c.boardID, limit)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("ошибка создания запроса: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.token))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ошибка выполнения запроса: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("неверный код ответа: %d", resp.StatusCode)
	}

	var result struct {
		Data []models.Task `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("ошибка декодирования ответа: %w", err)
	}

	// Обновляем кэш
	c.mu.Lock()
	c.cache.Tasks = make([]models.Task, len(result.Data))
	copy(c.cache.Tasks, result.Data)
	c.cache.UpdatedAt = time.Now()
	c.mu.Unlock()

	return result.Data, nil
}

// CreateTask создает новую задачу
func (c *Client) CreateTask(task *models.Task) error {
	url := fmt.Sprintf("https://yougile.com/api-v2/board/%s/tasks", c.boardID)

	data, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("ошибка сериализации задачи: %w", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("ошибка создания запроса: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.token))
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("ошибка выполнения запроса: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("неверный код ответа: %d", resp.StatusCode)
	}

	return nil
}

// UpdateTask обновляет существующую задачу
func (c *Client) UpdateTask(task *models.Task) error {
	url := fmt.Sprintf("https://yougile.com/api-v2/board/%s/tasks/%d", c.boardID, task.ID)

	data, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("ошибка сериализации задачи: %w", err)
	}

	req, err := http.NewRequest("PUT", url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("ошибка создания запроса: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.token))
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("ошибка выполнения запроса: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("неверный код ответа: %d", resp.StatusCode)
	}

	return nil
}

// UploadAttachment загружает вложение на сервер
func (c *Client) UploadAttachment(taskID int64, attachment *models.Attachment, data []byte) error {
	url := fmt.Sprintf("https://yougile.com/api-v2/board/%s/tasks/%d/attachments", c.boardID, taskID)

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Добавляем метаданные
	metaHeader := textproto.MIMEHeader{}
	metaHeader.Set("Content-Disposition", `form-data; name="metadata"`)
	metaHeader.Set("Content-Type", "application/json")
	metaPart, err := writer.CreatePart(metaHeader)
	if err != nil {
		return fmt.Errorf("ошибка создания части metadata: %w", err)
	}
	if err := json.NewEncoder(metaPart).Encode(attachment); err != nil {
		return fmt.Errorf("ошибка кодирования metadata: %w", err)
	}

	// Добавляем файл
	fileHeader := textproto.MIMEHeader{}
	fileHeader.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, attachment.ID))
	fileHeader.Set("Content-Type", "application/octet-stream")
	filePart, err := writer.CreatePart(fileHeader)
	if err != nil {
		return fmt.Errorf("ошибка создания части file: %w", err)
	}
	if _, err := filePart.Write(data); err != nil {
		return fmt.Errorf("ошибка записи файла: %w", err)
	}

	if err := writer.Close(); err != nil {
		return fmt.Errorf("ошибка закрытия writer: %w", err)
	}

	req, err := http.NewRequest("POST", url, body)
	if err != nil {
		return fmt.Errorf("ошибка создания запроса: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.token))
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("ошибка выполнения запроса: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("неверный код ответа: %d", resp.StatusCode)
	}

	return nil
}

// AddComment добавляет комментарий к задаче
func (c *Client) AddComment(taskID int64, comment *models.Comment) error {
	url := fmt.Sprintf("https://yougile.com/api-v2/board/%s/tasks/%d/comments", c.boardID, taskID)

	data, err := json.Marshal(comment)
	if err != nil {
		return fmt.Errorf("ошибка сериализации комментария: %w", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("ошибка создания запроса: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.token))
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("ошибка выполнения запроса: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("неверный код ответа: %d", resp.StatusCode)
	}

	return nil
}
