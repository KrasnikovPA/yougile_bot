// Package storage содержит методы управления пользователями в файлом хранилище.
package storage

import "yougile_bot4/internal/models"

// GetUserIDByUsername возвращает TelegramID пользователя по его username
func (s *Storage) GetUserIDByUsername(username string) int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.usersByUsername[username]
}

// UpdateUsername обновляет username пользователя
func (s *Storage) UpdateUsername(telegramID int64, username string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usersByUsername[username] = telegramID
}

// HasAdmins проверяет, есть ли хотя бы один администратор
func (s *Storage) HasAdmins() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, user := range s.users {
		if user.Role == models.RoleAdmin {
			return true
		}
	}
	return false
}
