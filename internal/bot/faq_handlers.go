package bot

import (
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

	// Отправляем ответ
	return c.Send(item.Answer, &telebot.SendOptions{
		ParseMode: telebot.ModeMarkdown,
	})
}
