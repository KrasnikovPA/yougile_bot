// Package api реализует клиент для взаимодействия с Yougile API.
package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"strconv"
	"strings"
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
	columnID   string
	// retry policy for GET requests
	retryCount int
	retryWait  time.Duration
	// max total time to spend retrying
	maxRetryElapsed time.Duration
	// verbose controls whether detailed response bodies (especially 404s) are logged.
	verbose bool
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
		verbose:         strings.ToLower(os.Getenv("YOUGILE_VERBOSE_LOG")) == "1" || strings.ToLower(os.Getenv("YOUGILE_VERBOSE_LOG")) == "true",
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

// SetColumnID задаёт columnId, который будет добавляться в запросы GetTasks.
func (c *Client) SetColumnID(column string) {
	c.columnID = column
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

	// Build request URL with proper query escaping. Some API instances are strict about query params.
	// Use net/url to avoid malformed URLs when boardID contains special characters.
	reqURL := fmt.Sprintf("%s/api-v2/tasks", c.baseURL)
	u, perr := url.Parse(reqURL)
	if perr != nil {
		return nil, fmt.Errorf("ошибка парсинга базового URL: %w", perr)
	}
	q := u.Query()
	q.Set("limit", strconv.Itoa(limit))
	// If columnId is provided, some instances expect columnId without boardId in the query.
	// Prefer omitting boardId when columnId is set to avoid 400 responses on some servers.
	if c.boardID != "" && c.columnID == "" {
		q.Set("boardId", c.boardID)
	}
	if c.columnID != "" {
		q.Set("columnId", c.columnID)
	}
	u.RawQuery = q.Encode()
	reqURL = u.String()

	// perform GET with retries/backoff for transient errors
	var resp *http.Response
	var err error
	triedBoardScoped := false
	triedTaskList := false
	for attempt := 0; attempt < c.retryCount; attempt++ {
		req, rerr := http.NewRequest("GET", reqURL, nil)
		if rerr != nil {
			err = fmt.Errorf("ошибка создания запроса: %w", rerr)
			break
		}
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.token))
		// some servers expect these headers even for GET; include for compatibility
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")

		if c.verbose {
			log.Printf("GetTasks request to %s", reqURL)
		}

		resp, err = c.httpClient.Do(req)
		if err == nil && resp != nil && resp.StatusCode == http.StatusOK {
			break
		}

		// read body if present for logging and error messages
		var bodyBytes []byte
		if resp != nil && resp.Body != nil {
			bodyBytes, _ = io.ReadAll(resp.Body)
			if cerr := resp.Body.Close(); cerr != nil {
				log.Printf("Ошибка закрытия тела ответа в GetTasks: %v", cerr)
			}
		}

		// consider retry on network error or 5xx or 429
		if err == nil {
			// err == nil but bad status -> decide whether to retry
			if resp == nil {
				log.Printf("GetTasks non-retriable: nil response")
				return nil, fmt.Errorf("неверный код ответа: nil")
			}
			// If we got a 400 on the general endpoint, first try /api-v2/task-list which newer instances use
			if resp.StatusCode == http.StatusBadRequest && !triedTaskList {
				if c.verbose {
					log.Printf("GetTasks: received 400 from %s, retrying with /api-v2/task-list", reqURL)
				}
				// build task-list URL. When columnId is set prefer column-only queries and never include boardId
				if c.columnID != "" {
					reqURL = fmt.Sprintf("%s/api-v2/task-list?columnId=%s&limit=%d", c.baseURL, url.PathEscape(c.columnID), limit)
				} else if c.boardID != "" {
					reqURL = fmt.Sprintf("%s/api-v2/task-list?boardId=%s&limit=%d", c.baseURL, url.PathEscape(c.boardID), limit)
				} else {
					reqURL = fmt.Sprintf("%s/api-v2/task-list?limit=%d", c.baseURL, limit)
				}
				triedTaskList = true
				continue
			}
			// If we got 400 and task-list already tried, attempt POST /api-v2/task-list with JSON body (some instances expect params in body)
			if resp.StatusCode == http.StatusBadRequest && !triedTaskList {
				// (redundant due to above, but keep safe) mark tried
				triedTaskList = true
			}
			// If we got 400 and task-list already tried, or task-list returned 400, try board-scoped path once
			// but skip board-scoped attempts when columnId is set (some servers reject board-scoped+columnId)
			if resp.StatusCode == http.StatusBadRequest && c.boardID != "" && !triedBoardScoped && c.columnID == "" {
				if c.verbose {
					log.Printf("GetTasks: received 400, retrying with board-scoped path /api-v2/board/{board}/tasks")
				}
				reqURL = fmt.Sprintf("%s/api-v2/board/%s/tasks?limit=%d", c.baseURL, url.PathEscape(c.boardID), limit)
				triedBoardScoped = true
				continue
			}
			if resp.StatusCode < 500 && resp.StatusCode != 429 {
				// If board-scoped returned 404, try task-list as a fallback (but only once)
				if resp.StatusCode == http.StatusNotFound && strings.Contains(reqURL, "/board/") && !triedTaskList {
					if c.verbose {
						log.Printf("GetTasks: board-scoped returned 404, retrying with /api-v2/task-list")
					}
					// prefer column-only task-list when columnId is set
					if c.columnID != "" {
						reqURL = fmt.Sprintf("%s/api-v2/task-list?columnId=%s&limit=%d", c.baseURL, url.PathEscape(c.columnID), limit)
					} else if c.boardID != "" {
						reqURL = fmt.Sprintf("%s/api-v2/task-list?boardId=%s&limit=%d", c.baseURL, url.PathEscape(c.boardID), limit)
					} else {
						reqURL = fmt.Sprintf("%s/api-v2/task-list?limit=%d", c.baseURL, limit)
					}
					triedTaskList = true
					continue
				}

				// non-retriable status: include body for diagnostics
				if c.verbose {
					log.Printf("GetTasks non-retriable response: status=%v body=%s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
				} else {
					log.Printf("GetTasks: error status=%v", resp.StatusCode)
				}

				// Persist last response body to file for offline debugging (best-effort)
				if c.verbose && len(bodyBytes) > 0 {
					fname := fmt.Sprintf("logs/yougile_gettasks_failure_%d.json", time.Now().Unix())
					if werr := os.WriteFile(fname, bodyBytes, 0644); werr == nil {
						log.Printf("GetTasks: saved last non-retriable response body to %s", fname)
					} else {
						log.Printf("GetTasks: failed to save response body to %s: %v", fname, werr)
					}
				}

				// If we received 400 (BadRequest) or 404, try POST /api-v2/task-list
				if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusNotFound {
					postURL := fmt.Sprintf("%s/api-v2/task-list", c.baseURL)
					bodyMap := map[string]interface{}{"limit": limit}
					// Only include boardId when columnId is not set. Some instances reject boardId+columnId combination.
					if c.columnID == "" && c.boardID != "" {
						bodyMap["boardId"] = c.boardID
					}
					if c.columnID != "" {
						bodyMap["columnId"] = c.columnID
					}
					pdata, _ := json.Marshal(bodyMap)
					if c.verbose {
						log.Printf("GetTasks: attempting POST %s body=%s", postURL, string(pdata))
					}
					preq, perr := http.NewRequest("POST", postURL, bytes.NewReader(pdata))
					if perr == nil {
						preq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.token))
						preq.Header.Set("Content-Type", "application/json")
						preq.Header.Set("Accept", "application/json")
						presp, perr2 := c.httpClient.Do(preq)
						if perr2 == nil && presp != nil {
							pbody, _ := io.ReadAll(presp.Body)
							_ = presp.Body.Close()
							if presp.StatusCode == http.StatusOK {
								var result struct {
									Data []models.Task `json:"data"`
								}
								if err := json.Unmarshal(pbody, &result); err == nil {
									c.mu.Lock()
									c.cache.Tasks = make([]models.Task, len(result.Data))
									copy(c.cache.Tasks, result.Data)
									c.cache.UpdatedAt = time.Now()
									c.mu.Unlock()
									return result.Data, nil
								}
							}
							if c.verbose {
								log.Printf("GetTasks POST /task-list returned status=%d body=%s", presp.StatusCode, strings.TrimSpace(string(pbody)))
							}
						} else {
							if c.verbose {
								log.Printf("GetTasks POST /task-list request failed: %v", perr2)
							}
						}
					}
				}

				// Before returning, try a short list of alternative GET endpoints once
				altPaths := []string{
					// try without boardId param
					fmt.Sprintf("%s/api-v2/tasks?limit=%d", c.baseURL, limit),
					// try alternative param name 'board'
					fmt.Sprintf("%s/api-v2/tasks?board=%s&limit=%d", c.baseURL, url.PathEscape(c.boardID), limit),
					// older endpoints
					fmt.Sprintf("%s/api/tasks?limit=%d", c.baseURL, limit),
					fmt.Sprintf("%s/api/tasks?boardId=%s&limit=%d", c.baseURL, url.PathEscape(c.boardID), limit),
					fmt.Sprintf("%s/api/tasks?board=%s&limit=%d", c.baseURL, url.PathEscape(c.boardID), limit),
					fmt.Sprintf("%s/api/task-list?boardId=%s&limit=%d", c.baseURL, url.PathEscape(c.boardID), limit),
					fmt.Sprintf("%s/api/task-list?board=%s&limit=%d", c.baseURL, url.PathEscape(c.boardID), limit),
					fmt.Sprintf("%s/api/tasks/list?boardId=%s&limit=%d", c.baseURL, url.PathEscape(c.boardID), limit),
				}
				for _, alt := range altPaths {
					if c.verbose {
						log.Printf("GetTasks: attempting alternative GET %s", alt)
					}
					areq, aerr := http.NewRequest("GET", alt, nil)
					if aerr != nil {
						if c.verbose {
							log.Printf("GetTasks alt request build failed: %v", aerr)
						}
						continue
					}
					areq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.token))
					areq.Header.Set("Accept", "application/json")
					apresp, aerr := c.httpClient.Do(areq)
					if aerr != nil {
						if c.verbose {
							log.Printf("GetTasks alt request failed: %v", aerr)
						}
						continue
					}
					apbody, _ := io.ReadAll(apresp.Body)
					_ = apresp.Body.Close()
					if apresp.StatusCode == http.StatusOK {
						var result struct {
							Data []models.Task `json:"data"`
						}
						if err := json.Unmarshal(apbody, &result); err == nil {
							c.mu.Lock()
							c.cache.Tasks = make([]models.Task, len(result.Data))
							copy(c.cache.Tasks, result.Data)
							c.cache.UpdatedAt = time.Now()
							c.mu.Unlock()
							return result.Data, nil
						}
						// If parsing of the alternative GET succeeded but unexpected shape, dump for inspection
						if c.verbose && len(apbody) > 0 {
							afname := fmt.Sprintf("logs/yougile_gettasks_alt_%d.json", time.Now().Unix())
							if werr := os.WriteFile(afname, apbody, 0644); werr == nil {
								log.Printf("GetTasks: saved alternative GET response to %s", afname)
							} else {
								log.Printf("GetTasks: failed to save alternative GET response: %v", werr)
							}
							log.Printf("GetTasks alt parse failed for %s: %v", alt, err)
						}
					} else {
						if c.verbose {
							log.Printf("GetTasks alt %s returned status=%d body=%s", alt, apresp.StatusCode, strings.TrimSpace(string(apbody)))
						}
					}
				}

				return nil, fmt.Errorf("неверный код ответа: %d, тело: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
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

	// Read full body so we can parse both into models.Task and inspect raw maps for extra fields
	bodyBytes, berr := io.ReadAll(resp.Body)
	if berr != nil {
		return nil, fmt.Errorf("ошибка чтения тела ответа: %w", berr)
	}

	var result struct {
		Data []models.Task `json:"data"`
	}
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return nil, fmt.Errorf("ошибка декодирования ответа: %w", err)
	}

	// Also try to parse into generic maps to extract non-standard fields like idTaskProject/idTaskCommon
	var raw struct {
		Data []map[string]interface{} `json:"data"`
	}
	_ = json.Unmarshal(bodyBytes, &raw) // non-fatal

	// For each returned task, if Key is empty try common alternate fields
	for i := range result.Data {
		if result.Data[i].Key == "" {
			// try to find corresponding raw map
			if i < len(raw.Data) {
				m := raw.Data[i]
				if v, ok := m["idTaskProject"].(string); ok && v != "" {
					result.Data[i].Key = v
				} else if v, ok := m["idTaskCommon"].(string); ok && v != "" {
					result.Data[i].Key = v
				} else if v, ok := m["key"].(string); ok && v != "" {
					result.Data[i].Key = v
				} else if v, ok := m["shortId"].(string); ok && v != "" {
					result.Data[i].Key = v
				} else if v, ok := m["number"].(string); ok && v != "" {
					result.Data[i].Key = v
				}
			}
		}
	}

	// Update cache
	c.mu.Lock()
	c.cache.Tasks = make([]models.Task, len(result.Data))
	copy(c.cache.Tasks, result.Data)
	c.cache.UpdatedAt = time.Now()
	c.mu.Unlock()

	return result.Data, nil
}

// CreateTask создает новую задачу
func (c *Client) CreateTask(task *models.Task) error {
	// Prefer board-scoped create endpoint when boardID is configured (some instances require it)
	reqURL := fmt.Sprintf("%s/api-v2/tasks", c.baseURL)
	if c.boardID != "" {
		reqURL = fmt.Sprintf("%s/api-v2/board/%s/tasks", c.baseURL, url.PathEscape(c.boardID))
	}
	// Build payload following CreateTaskDto from OpenAPI
	payload := make(map[string]interface{})
	// title is required by API
	payload["title"] = task.Title
	if task.Description != "" {
		payload["description"] = task.Description
	}
	if task.ColumnID != "" {
		payload["columnId"] = task.ColumnID
	}
	// assigned is intentionally not sent by the bot when creating tasks
	if !task.DueDate.IsZero() {
		// API expects deadline timestamp in milliseconds
		payload["deadline"] = map[string]interface{}{
			"deadline": task.DueDate.UnixMilli(),
			"withTime": true,
		}
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("ошибка сериализации задачи: %w", err)
	}
	// use retry helper
	err = c.retryOperation(func() (bool, error) {
		req, err := http.NewRequest("POST", reqURL, bytes.NewReader(data))
		if err != nil {
			return true, fmt.Errorf("ошибка создания запроса: %w", err)
		}
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.token))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")

		// log request payload for debugging
		log.Printf("CreateTask request to %s payload=%s", reqURL, strings.TrimSpace(string(data)))

		resp, err := c.httpClient.Do(req)
		if err != nil {
			// network error -> retry
			return false, fmt.Errorf("ошибка выполнения запроса: %w", err)
		}
		if resp == nil {
			return false, fmt.Errorf("пустой ответ от сервера")
		}

		// read full body for logging and parsing
		bodyBytes, _ := io.ReadAll(resp.Body)
		if cerr := resp.Body.Close(); cerr != nil {
			log.Printf("Ошибка закрытия тела ответа в CreateTask: %v", cerr)
		}

		log.Printf("CreateTask response status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))

		if resp.StatusCode == http.StatusCreated {
			// Try several common shapes: {"data": {"id": N}}, {"data": {"task": {"id": N}}}, {"id": N}
			var r1 struct {
				Data struct {
					ID int64 `json:"id"`
				} `json:"data"`
			}
			if err := json.Unmarshal(bodyBytes, &r1); err == nil && r1.Data.ID != 0 {
				task.ID = r1.Data.ID
				// if client has no columnID configured but task was created in a specific column,
				// adopt it so subsequent GetTasks will prefer column-only queries.
				if c.columnID == "" && task.ColumnID != "" {
					c.SetColumnID(task.ColumnID)
				}
				return true, nil
			}
			var r2 struct {
				Data struct {
					Task struct {
						ID int64 `json:"id"`
					} `json:"task"`
				} `json:"data"`
			}
			if err := json.Unmarshal(bodyBytes, &r2); err == nil && r2.Data.Task.ID != 0 {
				task.ID = r2.Data.Task.ID
				if c.columnID == "" && task.ColumnID != "" {
					c.SetColumnID(task.ColumnID)
				}
				return true, nil
			}
			// Try a generic unmarshal to catch both numeric and string ids
			var generic map[string]interface{}
			if err := json.Unmarshal(bodyBytes, &generic); err == nil {
				if v, ok := generic["id"]; ok && v != nil {
					switch vv := v.(type) {
					case float64:
						task.ID = int64(vv)
						if c.columnID == "" && task.ColumnID != "" {
							c.SetColumnID(task.ColumnID)
						}
						return true, nil
					case string:
						// store string id in ExternalID for UUID-like ids
						task.ExternalID = vv
						if c.columnID == "" && task.ColumnID != "" {
							c.SetColumnID(task.ColumnID)
						}
						return true, nil
					}
				}
			}
			// fallback: try to parse Location header for ID at end
			if loc := resp.Header.Get("Location"); loc != "" {
				// try to parse trailing number
				parts := strings.Split(strings.TrimRight(loc, "/"), "/")
				if len(parts) > 0 {
					if id, perr := strconv.ParseInt(parts[len(parts)-1], 10, 64); perr == nil && id != 0 {
						task.ID = id
						return true, nil
					}
				}
			}

			// If none parsed, treat as error (don't silently ignore)
			if c.metrics != nil {
				c.metrics.IncAPIErrors()
			}
			log.Printf("CreateTask: unexpected response body: %s", string(bodyBytes))
			return true, fmt.Errorf("не удалось распарсить ответ CreateTask: %s", strings.TrimSpace(string(bodyBytes)))
		}

		// For error statuses decide if retry
		if resp.StatusCode >= 500 || resp.StatusCode == 429 {
			return false, fmt.Errorf("неверный код ответа (повторяем): %d, body: %s", resp.StatusCode, string(bodyBytes))
		}
		return true, fmt.Errorf("неверный код ответа: %d, body: %s", resp.StatusCode, string(bodyBytes))
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
func (c *Client) UploadAttachment(taskID string, attachment *models.Attachment, data []byte) error {
	// Start with the general tasks endpoint. If that returns 404 for UUID ids,
	// we'll try to resolve a numeric id and use the board-scoped path.
	url := fmt.Sprintf("%s/api-v2/tasks/%s/attachments", c.baseURL, taskID)
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

		// If we got 404 for tasks/{uuid}/attachments, some instances require a numeric ID and board-scoped path.
		if resp.StatusCode == http.StatusNotFound && strings.Contains(taskID, "-") {
			// try to resolve numeric ID by ExternalID via GetTasks
			if numeric, rerr := c.resolveNumericIDFromExternal(taskID); rerr == nil && numeric != 0 {
				// retry using board-scoped path with numeric id
				boardURL := fmt.Sprintf("%s/api-v2/board/%s/tasks/%d/attachments", c.baseURL, c.boardID, numeric)

				// Rebuild multipart body for retry (don't reuse previous writer/buffer)
				body2 := &bytes.Buffer{}
				writer2 := multipart.NewWriter(body2)

				// metadata part
				metaHeader2 := textproto.MIMEHeader{}
				metaHeader2.Set("Content-Disposition", `form-data; name="metadata"`)
				metaHeader2.Set("Content-Type", "application/json")
				metaPart2, merr := writer2.CreatePart(metaHeader2)
				if merr != nil {
					return true, fmt.Errorf("ошибка создания части metadata (retry): %w", merr)
				}
				if merr := json.NewEncoder(metaPart2).Encode(attachment); merr != nil {
					return true, fmt.Errorf("ошибка кодирования metadata (retry): %w", merr)
				}

				// file part
				fileHeader2 := textproto.MIMEHeader{}
				fileHeader2.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, attachment.ID))
				fileHeader2.Set("Content-Type", "application/octet-stream")
				filePart2, ferr := writer2.CreatePart(fileHeader2)
				if ferr != nil {
					return true, fmt.Errorf("ошибка создания части file (retry): %w", ferr)
				}
				if _, ferr := filePart2.Write(data); ferr != nil {
					return true, fmt.Errorf("ошибка записи файла (retry): %w", ferr)
				}

				if cerr := writer2.Close(); cerr != nil {
					return true, fmt.Errorf("ошибка закрытия writer (retry): %w", cerr)
				}

				rreq, rerr := http.NewRequest("POST", boardURL, body2)
				if rerr != nil {
					return true, fmt.Errorf("ошибка создания повторного запроса: %w", rerr)
				}
				rreq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.token))
				rreq.Header.Set("Content-Type", writer2.FormDataContentType())

				rresp, rerr := c.httpClient.Do(rreq)
				if rerr != nil {
					return false, fmt.Errorf("ошибка выполнения повторного запроса: %w", rerr)
				}
				if rresp != nil && rresp.StatusCode == http.StatusCreated {
					if cerr := rresp.Body.Close(); cerr != nil {
						log.Printf("Ошибка закрытия тела ответа в UploadAttachment (retry): %v", cerr)
					}
					return true, nil
				}
				if rresp != nil {
					var rb bytes.Buffer
					_, _ = rb.ReadFrom(rresp.Body)
					if cerr := rresp.Body.Close(); cerr != nil {
						log.Printf("Ошибка закрытия тела ответа в UploadAttachment (retry error): %v", cerr)
					}
					return true, fmt.Errorf("неверный код ответа при retry: %d, body: %s", rresp.StatusCode, rb.String())
				}
			}
		}

		if resp.StatusCode >= 500 || resp.StatusCode == 429 {
			return false, fmt.Errorf("неверный код ответа (повторяем): %d, body: %s", resp.StatusCode, bodyBuf.String())
		}
		return true, fmt.Errorf("неверный код ответа: %d, body: %s", resp.StatusCode, bodyBuf.String())
	})
}

// AddComment добавляет комментарий к задаче
func (c *Client) AddComment(taskID string, comment *models.Comment) error {
	url := fmt.Sprintf("%s/api-v2/tasks/%s/comments", c.baseURL, taskID)
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

		// If comment endpoint with UUID returned 404, try resolve numeric id and post to board-scoped path
		if resp.StatusCode == http.StatusNotFound && strings.Contains(taskID, "-") {
			if numeric, rerr := c.resolveNumericIDFromExternal(taskID); rerr == nil && numeric != 0 {
				boardURL := fmt.Sprintf("%s/api-v2/board/%s/tasks/%d/comments", c.baseURL, c.boardID, numeric)
				rreq, rerr := http.NewRequest("POST", boardURL, bytes.NewReader(data))
				if rerr != nil {
					return true, fmt.Errorf("ошибка создания повторного запроса: %w", rerr)
				}
				rreq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.token))
				rreq.Header.Set("Content-Type", "application/json")
				rresp, rerr := c.httpClient.Do(rreq)
				if rerr != nil {
					return false, fmt.Errorf("ошибка выполнения повторного запроса: %w", rerr)
				}
				if rresp != nil && rresp.StatusCode == http.StatusCreated {
					if cerr := rresp.Body.Close(); cerr != nil {
						log.Printf("Ошибка закрытия тела ответа в AddComment (retry): %v", cerr)
					}
					return true, nil
				}
				if rresp != nil {
					var rb bytes.Buffer
					_, _ = rb.ReadFrom(rresp.Body)
					if cerr := rresp.Body.Close(); cerr != nil {
						log.Printf("Ошибка закрытия тела ответа в AddComment (retry error): %v", cerr)
					}
					return true, fmt.Errorf("неверный код ответа при retry: %d, body: %s", rresp.StatusCode, rb.String())
				}
			}
		}
		if resp.StatusCode >= 500 || resp.StatusCode == 429 {
			return false, fmt.Errorf("неверный код ответа (повторяем): %d", resp.StatusCode)
		}
		return true, fmt.Errorf("неверный код ответа: %d", resp.StatusCode)
	})
}

// resolveNumericIDFromExternal пытается найти числовой ID задачи по её строковому ExternalID (UUID).
// Возвращает numeric ID или ошибку.
func (c *Client) resolveNumericIDFromExternal(external string) (int64, error) {
	// Попробуем получить последние задачи (ограничение небольшое)
	tasks, err := c.GetTasks(200)
	if err != nil {
		return 0, fmt.Errorf("ошибка получения задач для разрешения внешнего id: %w", err)
	}
	for _, t := range tasks {
		if t.ExternalID == external {
			return t.ID, nil
		}
		// также сравним с полем id в виде строки на случай, если API вернул id в data
		if fmt.Sprintf("%d", t.ID) == external {
			return t.ID, nil
		}
	}
	return 0, fmt.Errorf("не найден numeric id для external id: %s", external)
}

// GetTaskByID получает одну задачу по строковому ID (может быть UUID или numeric string).
// internal helper: getTaskByID performs the actual request and parsing.
// quiet=true suppresses request/404 logging (used by background scanners).
func (c *Client) getTaskByID(id string, quiet bool) (*models.Task, error) {
	// Try general endpoint first
	urls := []string{
		fmt.Sprintf("%s/api-v2/tasks/%s", c.baseURL, url.PathEscape(id)),
	}
	// board-scoped fallback
	if c.boardID != "" {
		urls = append(urls, fmt.Sprintf("%s/api-v2/board/%s/tasks/%s", c.baseURL, url.PathEscape(c.boardID), url.PathEscape(id)))
	}

	var lastErr error
	for _, u := range urls {
		req, err := http.NewRequest("GET", u, nil)
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.token))
		req.Header.Set("Accept", "application/json")
		if !quiet {
			log.Printf("GetTaskByID request to %s", u)
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp == nil {
			lastErr = fmt.Errorf("пустой ответ от сервера")
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if c.verbose {
			log.Printf("GetTaskByID response from %s status=%d body=%s", u, resp.StatusCode, strings.TrimSpace(string(body)))
		} else {
			// When quiet, suppress logging for 404 responses to avoid flooding logs during scans.
			if resp.StatusCode == http.StatusNotFound {
				if !quiet {
					log.Printf("GetTaskByID response from %s status=%d (not found)", u, resp.StatusCode)
				}
			} else {
				if !quiet {
					log.Printf("GetTaskByID response from %s status=%d (use YOUGILE_VERBOSE_LOG=1 for body)", u, resp.StatusCode)
				}
			}
		}
		if resp.StatusCode == http.StatusOK {
			// Save successful GetTaskByID body for inspection (best-effort)
			if len(body) > 0 {
				fname := fmt.Sprintf("logs/yougile_gettaskbyid_%d.json", time.Now().Unix())
				if werr := os.WriteFile(fname, body, 0644); werr == nil {
					log.Printf("GetTaskByID: saved response body to %s", fname)
				} else {
					log.Printf("GetTaskByID: failed to save response body: %v", werr)
				}
			}
			// Try different shapes: {data: task} or task directly
			var container struct {
				Data models.Task `json:"data"`
			}
			if err := json.Unmarshal(body, &container); err == nil {
				// If container has useful data (ID or ExternalID or Title) return it
				if container.Data.ID != 0 || container.Data.ExternalID != "" || container.Data.Title != "" {
					return &container.Data, nil
				}
			}
			var t models.Task
			if err := json.Unmarshal(body, &t); err == nil {
				if t.ID != 0 || t.ExternalID != "" || t.Title != "" {
					return &t, nil
				}
			}

			// Fallback: parse generic map and construct Task (handle string 'id' UUIDs)
			var m map[string]interface{}
			if err := json.Unmarshal(body, &m); err == nil {
				t2 := &models.Task{}
				if v, ok := m["id"]; ok && v != nil {
					switch vv := v.(type) {
					case string:
						t2.ExternalID = vv
					case float64:
						t2.ID = int64(vv)
					}
				}
				// Try common possible short-key fields used by Yougile instances
				if v, ok := m["idTaskProject"].(string); ok && v != "" {
					t2.Key = v
				} else if v, ok := m["idTaskCommon"].(string); ok && v != "" {
					t2.Key = v
				} else if v, ok := m["key"].(string); ok && v != "" {
					t2.Key = v
				} else if v, ok := m["shortId"].(string); ok && v != "" {
					t2.Key = v
				} else if v, ok := m["number"].(string); ok && v != "" {
					t2.Key = v
				}
				if v, ok := m["title"].(string); ok {
					t2.Title = v
				}
				if v, ok := m["description"].(string); ok {
					t2.Description = v
				}
				if v, ok := m["columnId"].(string); ok {
					t2.ColumnID = v
				} else if v, ok := m["column_id"].(string); ok {
					t2.ColumnID = v
				}
				if v, ok := m["completed"].(bool); ok {
					t2.Done = v
				}
				if v, ok := m["timestamp"]; ok {
					// timestamp may be float64
					switch tv := v.(type) {
					case float64:
						t2.CreatedAt = time.UnixMilli(int64(tv))
					case int64:
						t2.CreatedAt = time.UnixMilli(tv)
					}
				}
				// If we have at least title or id, return constructed task
				if t2.Title != "" || t2.ExternalID != "" || t2.ID != 0 {
					return t2, nil
				}
			}

			// If parsing failed, return error with body
			lastErr = fmt.Errorf("не удалось распарсить ответ GetTaskByID: %s", strings.TrimSpace(string(body)))
			continue
		}
		if resp.StatusCode == http.StatusNotFound {
			// When quiet, don't clutter logs with 404s — just record lastErr and continue
			lastErr = fmt.Errorf("не найдено: %s", u)
			continue
		}
		lastErr = fmt.Errorf("неверный код ответа %d при запросе %s, тело: %s", resp.StatusCode, u, strings.TrimSpace(string(body)))
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("не удалось получить задачу %s", id)
}

// GetTaskByID is the public API (non-quiet) that logs request/response as before.
func (c *Client) GetTaskByID(id string) (*models.Task, error) {
	return c.getTaskByID(id, false)
}

// GetTaskByIDQuiet performs the same logic but suppresses request/404 logs for background scans.
func (c *Client) GetTaskByIDQuiet(id string) (*models.Task, error) {
	return c.getTaskByID(id, true)
}
