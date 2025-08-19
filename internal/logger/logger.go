// Package logger предоставляет простую обёртку для логирования в файл.
package logger

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// RotateWriter реализует ротацию логов по размеру и времени.
// Используется как io.Writer для логирования с автоматическим архивированием.
type RotateWriter struct {
	filename    string
	maxSize     int64
	maxAge      time.Duration
	currentSize int64
	file        *os.File
	created     time.Time
}

// NewRotateWriter создает новый RotateWriter для указанного файла.
// maxSize — максимальный размер файла перед ротацией, maxAge — максимальный возраст файла.
func NewRotateWriter(filename string, maxSize int64, maxAge time.Duration) (*RotateWriter, error) {
	w := &RotateWriter{
		filename: filename,
		maxSize:  maxSize,
		maxAge:   maxAge,
	}
	if err := w.openFile(); err != nil {
		return nil, err
	}
	return w, nil
}

// Write записывает данные в файл и выполняет ротацию при необходимости.
func (w *RotateWriter) Write(p []byte) (n int, err error) {
	if w.file == nil {
		if err := w.openFile(); err != nil {
			return 0, err
		}
	}

	// Проверяем необходимость ротации
	if w.shouldRotate(int64(len(p))) {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}

	n, err = w.file.Write(p)
	w.currentSize += int64(n)
	return n, err
}

// Close закрывает текущий файл логов.
func (w *RotateWriter) Close() error {
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

// shouldRotate проверяет необходимость ротации
func (w *RotateWriter) shouldRotate(size int64) bool {
	if w.maxSize > 0 && w.currentSize+size > w.maxSize {
		return true
	}
	if w.maxAge > 0 && time.Since(w.created) > w.maxAge {
		return true
	}
	return false
}

// rotate выполняет ротацию файла лога
func (w *RotateWriter) rotate() error {
	if err := w.Close(); err != nil {
		return err
	}

	// Формируем имя для архивного файла
	timestamp := time.Now().Format("2006-01-02_15-04-05")
	dir := filepath.Dir(w.filename)
	base := filepath.Base(w.filename)
	ext := filepath.Ext(base)
	name := base[:len(base)-len(ext)]
	archived := filepath.Join(dir, fmt.Sprintf("%s_%s%s", name, timestamp, ext))

	// Переименовываем текущий файл
	if err := os.Rename(w.filename, archived); err != nil && !os.IsNotExist(err) {
		return err
	}

	return w.openFile()
}

// openFile открывает новый файл для записи
func (w *RotateWriter) openFile() error {
	dir := filepath.Dir(w.filename)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	f, err := os.OpenFile(w.filename, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}

	info, err := f.Stat()
	if err != nil {
		if cerr := f.Close(); cerr != nil {
			// Пытаемся закрыть файл и логируем, если не удалось
			fmt.Printf("Ошибка закрытия файла логов после Stat failure: %v\n", cerr)
		}
		return err
	}

	w.file = f
	w.currentSize = info.Size()
	w.created = time.Now()
	return nil
}

// GetWriter создает и возвращает реализующий io.Writer (RotateWriter) для логирования.
func GetWriter(filename string, maxSize int64, maxAge time.Duration) (io.Writer, error) {
	return NewRotateWriter(filename, maxSize, maxAge)
}
