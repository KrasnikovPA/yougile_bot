// Package storage реализует простое JSON-хранилище для данных бота.
package storage

import (
	"encoding/json"
	"os"
	"yougile_bot4/internal/models"
)

// LoadFAQ загружает данные FAQ из файла
func (s *Storage) LoadFAQ() error {
	data, err := os.ReadFile("data/faq.json")
	if err != nil {
		return err
	}

	var faq models.FAQData
	if err := json.Unmarshal(data, &faq); err != nil {
		return err
	}

	s.faq = faq
	return nil
}

// GetFAQItem возвращает элемент FAQ по ключу
func (s *Storage) GetFAQItem(key string) (models.FAQItem, bool) {
	item, exists := s.faq[key]
	return item, exists
}

// GetAllFAQItems возвращает все элементы FAQ
func (s *Storage) GetAllFAQItems() models.FAQData {
	return s.faq
}
