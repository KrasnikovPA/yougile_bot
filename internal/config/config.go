// Package config читает и хранит конфигурацию приложения.
package config

import (
	"fmt"
	"time"
)

// Config представляет конфигурацию приложения, загружаемую из YAML или окружения.
type Config struct {
	API struct {
		Yougile struct {
			Token     string        `yaml:"token"`
			Board     string        `yaml:"board"`
			Timeout   time.Duration `yaml:"timeout"`
			RateLimit int           `yaml:"rate_limit"`
		} `yaml:"yougile"`
		Telegram struct {
			Token   string        `yaml:"token"`
			Timeout time.Duration `yaml:"timeout"`
		} `yaml:"telegram"`
	} `yaml:"api"`

	Storage struct {
		Path         string        `yaml:"path"`
		SaveInterval time.Duration `yaml:"save_interval"`
	} `yaml:"storage"`

	Logging struct {
		Level     string `yaml:"level"`
		File      string `yaml:"file"`
		MaxSize   int    `yaml:"max_size"`
		MaxAge    int    `yaml:"max_age"`
		MaxBackup int    `yaml:"max_backup"`
	} `yaml:"logging"`

	Bot struct {
		MinMessageLength int           `yaml:"min_message_length"`
		RegTimeout       time.Duration `yaml:"reg_timeout"`
		TaskLimit        int           `yaml:"task_limit"`
		CheckInterval    time.Duration `yaml:"check_interval"`
	} `yaml:"bot"`
}

// NewConfig создает и возвращает конфигурацию с безопасными значениями по умолчанию.
func NewConfig() *Config {
	cfg := &Config{}

	// API настройки по умолчанию
	cfg.API.Yougile.Timeout = 30 * time.Second
	cfg.API.Yougile.RateLimit = 100
	cfg.API.Telegram.Timeout = 30 * time.Second

	// Storage настройки по умолчанию
	cfg.Storage.Path = "data"
	cfg.Storage.SaveInterval = 15 * time.Minute

	// Logging настройки по умолчанию
	cfg.Logging.Level = "info"
	cfg.Logging.File = "logs/bot.log"
	cfg.Logging.MaxSize = 10 // МБ
	cfg.Logging.MaxAge = 30  // дней
	cfg.Logging.MaxBackup = 5

	// Bot настройки по умолчанию
	cfg.Bot.MinMessageLength = 10
	cfg.Bot.RegTimeout = 24 * time.Hour
	cfg.Bot.TaskLimit = 100
	cfg.Bot.CheckInterval = 5 * time.Minute

	return cfg
}

// Validate проверяет обязательные поля конфигурации и возвращает ошибку при отсутствии.
func (c *Config) Validate() error {
	if c.API.Yougile.Token == "" {
		return fmt.Errorf("не указан токен Yougile")
	}
	if c.API.Yougile.Board == "" {
		return fmt.Errorf("не указан ID доски Yougile")
	}
	if c.API.Telegram.Token == "" {
		return fmt.Errorf("не указан токен Telegram")
	}
	return nil
}
