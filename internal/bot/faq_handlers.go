// Package bot содержит обработчики для FAQ и утилиты бота.
package bot

import (
	"strings"

	"gopkg.in/telebot.v3"
)

// handleFAQ обрабатывает запрос к FAQ
func (b *Bot) handleFAQ(c telebot.Context) error {
	// Создаем клавиатуру с вопросами из FAQ
	menu := &telebot.ReplyMarkup{ResizeKeyboard: true}

	// Получаем все элементы FAQ
	faqItems := b.storage.GetAllFAQItems()

	// Создаем кнопки для каждого вопроса
	var rows []telebot.Row
	for key, item := range faqItems {
		btn := menu.Data(item.Question, "faq", key)
		rows = append(rows, menu.Row(btn))
	}

	// Добавляем кнопку "Назад"
	rows = append(rows, menu.Row(btnBack))
	menu.Inline(rows...)

	return c.Send("Выберите интересующий вас вопрос:", menu)
}

// handleFAQCallback обрабатывает нажатие на кнопку FAQ
func (b *Bot) handleFAQCallback(c telebot.Context) error {
	// Получаем ключ FAQ из данных callback
	key := c.Data()

	// Получаем элемент FAQ
	item, exists := b.storage.GetFAQItem(key)
	if !exists {
		return c.Send("Извините, информация по этому вопросу не найдена.")
	}

	// Если ответ содержит слово "парол" (пароль), обернём в моноширинный формат
	// только ту часть строки, где упоминается пароль (после двоеточия в соответствующей строке).
	answer := item.Answer
	lower := strings.ToLower(answer)
	if strings.Contains(lower, "парол") {
		// Разбиваем на строки и ищем ту, где упоминается пароль
		lines := strings.Split(answer, "\n")
		for i, line := range lines {
			if strings.Contains(strings.ToLower(line), "парол") {
				if idx := strings.Index(line, ":"); idx != -1 && idx+1 < len(line) {
					before := line[:idx+1]
					after := strings.TrimSpace(line[idx+1:])
					// Если пароль уже помечен backticks в faq.json — не оборачиваем повторно.
					if strings.Contains(after, "`") {
						lines[i] = before + " " + after
					} else {
						// Оборачиваем только значение пароля в моноширинный блок
						lines[i] = before + " `" + after + "`"
					}
				}
				break
			}
		}
		answer = strings.Join(lines, "\n")
	}

	return c.Send(answer, &telebot.SendOptions{
		ParseMode: telebot.ModeMarkdown,
	})
}
