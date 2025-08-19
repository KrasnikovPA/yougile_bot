// Package api реализует клиент для взаимодействия с Yougile API.
package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"sync"
	"time"

	"yougile_bot4/internal/metrics"
	"yougile_bot4/internal/models"
)

var (
	// ErrUnauthorized возвращается при ошибке авторизации с API Yougile.
	// ErrUnauthorized возвращается при ошибке авторизации с API Yougile.
	ErrUnauthorized = fmt.Errorf("unauthorized")
	// ErrNotFound возвращается, когда запрашиваемый ресурс не найден в Yougile API.
	ErrNotFound = fmt.Errorf("not found")
	// ErrRateLimit возвращается при превышении лимита запросов к API.
	ErrRateLimit = fmt.Errorf("rate limit exceeded")
)

// TaskCache представляет кэш задач, используемый клиентом для уменьшения количества запросов.
type TaskCache struct {
	Tasks      []models.Task
	UpdatedAt  time.Time
	Expiration time.Duration
}

// retryOperation выполняет операцию с retry/backoff.
// Функция op должна возвращать (done, err) где done=true означает, что операция завершена (успех или не‑повторяемая ошибка).
func (c *Client) retryOperation(op func() (bool, error)) error {
	start := time.Now()
	var lastErr error
	for attempt := 0; attempt < c.retryCount; attempt++ {
		done, err := op()
		if err == nil && done {
			return nil
		}
		if err != nil && done {
			// non-retryable error
			return err
		}
		if err != nil {
			lastErr = err
		}

		if c.maxRetryElapsed > 0 && time.Since(start) > c.maxRetryElapsed {
			lastErr = fmt.Errorf("превышено максимальное время повторов: %v", c.maxRetryElapsed)
			break
		}

		backoff := c.retryWait * (1 << attempt)
		jitter := time.Duration(rand.Int63n(int64(c.retryWait)))
		time.Sleep(backoff + jitter)
	}
	if lastErr != nil {
		log.Printf("yougile client: operation failed after retries: %v", lastErr)
		return lastErr
	}
	return fmt.Errorf("операция не удалась после попыток")
}

// Client представляет HTTP-клиент для взаимодействия с Yougile API.
// Он реализует retry/backoff и локальный кэш задач.
type Client struct {
	token      string
	boardID    string
	httpClient *http.Client
	cache      *TaskCache
	mu         sync.RWMutex
	metrics    *metrics.Metrics
	baseURL    string
	// retry policy for GET requests
	retryCount int
	retryWait  time.Duration
	// max total time to spend retrying
	maxRetryElapsed time.Duration
}

// NewClient создает новый экземпляр Client.
// token — токен доступа Yougile, boardID — идентификатор доски, timeout — HTTP timeout.
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
		metrics:         m,
		baseURL:         "https://yougile.com",
		retryCount:      3,
		retryWait:       500 * time.Millisecond,
		maxRetryElapsed: 10 * time.Second,
	}
}

// SetRetryPolicy задаёт политику повторов для запросов клиента.
// count — максимальное число попыток, wait — базовый интервал между повторами,
// maxElapsed — максимальное суммарное время на повторы.
func (c *Client) SetRetryPolicy(count int, wait, maxElapsed time.Duration) {
	if count > 0 {
		c.retryCount = count
	}
	if wait > 0 {
		c.retryWait = wait
	}
	if maxElapsed > 0 {
		c.maxRetryElapsed = maxElapsed
	}
}

// GetTasks получает список задач с доски
func (c *Client) GetTasks(limit int) ([]models.Task, error) {
	start := time.Now()
	if c.metrics != nil {
		c.metrics.IncAPIRequests()
	}
	defer func() {
		if c.metrics != nil {
			c.metrics.UpdateLatency(time.Since(start))
		}
	}()

	c.mu.RLock()
	if c.cache != nil && len(c.cache.Tasks) > 0 && time.Since(c.cache.UpdatedAt) < c.cache.Expiration {
		tasks := make([]models.Task, len(c.cache.Tasks))
		copy(tasks, c.cache.Tasks)
		c.mu.RUnlock()
		return tasks, nil
	}
	c.mu.RUnlock()

	url := fmt.Sprintf("%s/api-v2/board/%s/tasks?limit=%d", c.baseURL, c.boardID, limit)

	// perform GET with retries/backoff for transient errors
	var resp *http.Response
	var err error
	for attempt := 0; attempt < c.retryCount; attempt++ {
		req, rerr := http.NewRequest("GET", url, nil)
		if rerr != nil {
			err = fmt.Errorf("ошибка создания запроса: %w", rerr)
			break
		}
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.token))

		resp, err = c.httpClient.Do(req)
		if err == nil && resp != nil && resp.StatusCode == http.StatusOK {
			break
		}

		// Close body if present and decide whether to retry
		if resp != nil && resp.Body != nil {
			if cerr := resp.Body.Close(); cerr != nil {
				log.Printf("Ошибка закрытия тела ответа в GetTasks: %v", cerr)
			}
		}

		// consider retry on network error or 5xx or 429
		if err == nil {
			// err == nil but bad status -> decide whether to retry
			if resp == nil || (resp.StatusCode < 500 && resp.StatusCode != 429) {
				// non-retriable status
				return nil, fmt.Errorf("неверный код ответа: %d", resp.StatusCode)
			}
			// otherwise: 5xx or 429 -> retry
		}

		// sleep with exponential backoff + jitter
		backoff := c.retryWait * (1 << attempt)
		jitter := time.Duration(rand.Int63n(int64(c.retryWait)))
		time.Sleep(backoff + jitter)
	}
	if err != nil {
		return nil, fmt.Errorf("ошибка выполнения запроса: %w", err)
	}
	if resp == nil {
		return nil, fmt.Errorf("ошибка выполнения запроса: пустой ответ")
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			log.Printf("Ошибка закрытия тела ответа в GetTasks (defer): %v", cerr)
		}
	}()

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
	url := fmt.Sprintf("%s/api-v2/board/%s/tasks", c.baseURL, c.boardID)
	data, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("ошибка сериализации задачи: %w", err)
	}
	// use retry helper
	err = c.retryOperation(func() (bool, error) {
		req, err := http.NewRequest("POST", url, bytes.NewReader(data))
		if err != nil {
			return true, fmt.Errorf("ошибка создания запроса: %w", err)
		}
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.token))
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			// network error -> retry
			return false, fmt.Errorf("ошибка выполнения запроса: %w", err)
		}
		if resp == nil {
			return false, fmt.Errorf("пустой ответ от сервера")
		}

		if resp.StatusCode == http.StatusCreated {
			var result struct {
				Data struct {
					ID int64 `json:"id"`
				} `json:"data"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				if c.metrics != nil {
					c.metrics.IncAPIErrors()
				}
				if cerr := resp.Body.Close(); cerr != nil {
					log.Printf("Ошибка закрытия тела ответа в CreateTask после Decode failure: %v", cerr)
				}
				return true, nil // treat as success
			}
			if result.Data.ID != 0 {
				task.ID = result.Data.ID
			}
			if cerr := resp.Body.Close(); cerr != nil {
				log.Printf("Ошибка закрытия тела ответа в CreateTask: %v", cerr)
			}
			return true, nil
		}

		var bodyBuf bytes.Buffer
		_, _ = bodyBuf.ReadFrom(resp.Body)
		if cerr := resp.Body.Close(); cerr != nil {
			log.Printf("Ошибка закрытия тела ответа в CreateTask (error path): %v", cerr)
		}
		if resp.StatusCode >= 500 || resp.StatusCode == 429 {
			return false, fmt.Errorf("неверный код ответа (повторяем): %d, body: %s", resp.StatusCode, bodyBuf.String())
		}
		return true, fmt.Errorf("неверный код ответа: %d, body: %s", resp.StatusCode, bodyBuf.String())
	})

	return err
}

// UpdateTask обновляет существующую задачу
func (c *Client) UpdateTask(task *models.Task) error {
	url := fmt.Sprintf("%s/api-v2/board/%s/tasks/%d", c.baseURL, c.boardID, task.ID)
	data, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("ошибка сериализации задачи: %w", err)
	}

	// use retry helper for update
	return c.retryOperation(func() (bool, error) {
		req, err := http.NewRequest("PUT", url, bytes.NewReader(data))
		if err != nil {
			return true, fmt.Errorf("ошибка создания запроса: %w", err)
		}
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.token))
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return false, fmt.Errorf("ошибка выполнения запроса: %w", err)
		}
		if resp.Body != nil {
			var bodyBuf bytes.Buffer
			_, _ = bodyBuf.ReadFrom(resp.Body)
			if cerr := resp.Body.Close(); cerr != nil {
				log.Printf("Ошибка закрытия тела ответа в UpdateTask: %v", cerr)
			}
		}
		if resp.StatusCode == http.StatusOK {
			return true, nil
		}
		if resp.StatusCode >= 500 || resp.StatusCode == 429 {
			return false, fmt.Errorf("неверный код ответа (повторяем): %d", resp.StatusCode)
		}
		return true, fmt.Errorf("неверный код ответа: %d", resp.StatusCode)
	})
}

// UploadAttachment загружает вложение на сервер
func (c *Client) UploadAttachment(taskID int64, attachment *models.Attachment, data []byte) error {
	url := fmt.Sprintf("%s/api-v2/board/%s/tasks/%d/attachments", c.baseURL, c.boardID, taskID)
	return c.retryOperation(func() (bool, error) {
		// build multipart body per attempt (buffer is consumed by request)
		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)

		// Добавляем метаданные
		metaHeader := textproto.MIMEHeader{}
		metaHeader.Set("Content-Disposition", `form-data; name="metadata"`)
		metaHeader.Set("Content-Type", "application/json")
		metaPart, err := writer.CreatePart(metaHeader)
		if err != nil {
			return true, fmt.Errorf("ошибка создания части metadata: %w", err)
		}
		if err := json.NewEncoder(metaPart).Encode(attachment); err != nil {
			return true, fmt.Errorf("ошибка кодирования metadata: %w", err)
		}

		// Добавляем файл
		fileHeader := textproto.MIMEHeader{}
		fileHeader.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, attachment.ID))
		fileHeader.Set("Content-Type", "application/octet-stream")
		filePart, err := writer.CreatePart(fileHeader)
		if err != nil {
			return true, fmt.Errorf("ошибка создания части file: %w", err)
		}
		if _, err := filePart.Write(data); err != nil {
			return true, fmt.Errorf("ошибка записи файла: %w", err)
		}

		if err := writer.Close(); err != nil {
			return true, fmt.Errorf("ошибка закрытия writer: %w", err)
		}

		req, err := http.NewRequest("POST", url, body)
		if err != nil {
			return true, fmt.Errorf("ошибка создания запроса: %w", err)
		}

		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.token))
		req.Header.Set("Content-Type", writer.FormDataContentType())

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return false, fmt.Errorf("ошибка выполнения запроса: %w", err)
		}
		if resp == nil {
			return false, fmt.Errorf("пустой ответ от сервера")
		}
		if resp.StatusCode == http.StatusCreated {
			if cerr := resp.Body.Close(); cerr != nil {
				log.Printf("Ошибка закрытия тела ответа в UploadAttachment: %v", cerr)
			}
			return true, nil
		}
		var bodyBuf bytes.Buffer
		_, _ = bodyBuf.ReadFrom(resp.Body)
		if cerr := resp.Body.Close(); cerr != nil {
			log.Printf("Ошибка закрытия тела ответа в UploadAttachment (error path): %v", cerr)
		}
		if resp.StatusCode >= 500 || resp.StatusCode == 429 {
			return false, fmt.Errorf("неверный код ответа (повторяем): %d, body: %s", resp.StatusCode, bodyBuf.String())
		}
		return true, fmt.Errorf("неверный код ответа: %d, body: %s", resp.StatusCode, bodyBuf.String())
	})
}

// AddComment добавляет комментарий к задаче
func (c *Client) AddComment(taskID int64, comment *models.Comment) error {
	url := fmt.Sprintf("%s/api-v2/board/%s/tasks/%d/comments", c.baseURL, c.boardID, taskID)
	data, err := json.Marshal(comment)
	if err != nil {
		return fmt.Errorf("ошибка сериализации комментария: %w", err)
	}

	return c.retryOperation(func() (bool, error) {
		req, err := http.NewRequest("POST", url, bytes.NewReader(data))
		if err != nil {
			return true, fmt.Errorf("ошибка создания запроса: %w", err)
		}
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.token))
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return false, fmt.Errorf("ошибка выполнения запроса: %w", err)
		}
		if resp == nil {
			return false, fmt.Errorf("пустой ответ от сервера")
		}
		var bodyBuf bytes.Buffer
		_, _ = bodyBuf.ReadFrom(resp.Body)
		if cerr := resp.Body.Close(); cerr != nil {
			log.Printf("Ошибка закрытия тела ответа в AddComment: %v", cerr)
		}
		if resp.StatusCode == http.StatusCreated {
			return true, nil
		}
		if resp.StatusCode >= 500 || resp.StatusCode == 429 {
			return false, fmt.Errorf("неверный код ответа (повторяем): %d", resp.StatusCode)
		}
		return true, fmt.Errorf("неверный код ответа: %d", resp.StatusCode)
	})
}
