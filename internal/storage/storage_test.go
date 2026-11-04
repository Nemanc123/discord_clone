package storage_test

// Проверка хранилища требует запущенного S3-совместимого сервера.
// Без него тесты пропускаются.

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/Nemanc123/discord_clone/internal/storage"
)

func newStorage(t *testing.T) *storage.Storage {
	t.Helper()

	endpoint := os.Getenv("STORAGE_ENDPOINT")
	if endpoint == "" {
		endpoint = "localhost:8333"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	st, err := storage.New(ctx, storage.Config{
		Endpoint:  endpoint,
		AccessKey: envOr("STORAGE_ACCESS_KEY", "dcstorage"),
		SecretKey: envOr("STORAGE_SECRET_KEY", "devstorage123"),
		Bucket:    "discord-clone-test",
	})
	if err != nil {
		t.Skipf("хранилище недоступно: %v", err)
	}

	return st
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func TestUploadAndDownload(t *testing.T) {
	st := newStorage(t)
	ctx := context.Background()

	key := "attachments/test/hello.txt"
	payload := []byte("привет из теста")

	uploadURL, err := st.PresignedPut(ctx, key)
	if err != nil {
		t.Fatalf("ссылка на загрузку: %v", err)
	}

	req, err := http.NewRequest(http.MethodPut, uploadURL, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("запрос загрузки: %v", err)
	}
	req.Header.Set("Content-Type", "text/plain")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("загрузка: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		t.Fatalf("загрузка вернула %d", resp.StatusCode)
	}

	exists, err := st.Exists(ctx, key)
	if err != nil {
		t.Fatalf("проверка объекта: %v", err)
	}
	if !exists {
		t.Fatal("объект не найден после загрузки")
	}

	downloadURL, err := st.PresignedGet(ctx, key)
	if err != nil {
		t.Fatalf("ссылка на скачивание: %v", err)
	}

	got, err := http.Get(downloadURL)
	if err != nil {
		t.Fatalf("скачивание: %v", err)
	}
	defer func() { _ = got.Body.Close() }()

	body, err := io.ReadAll(got.Body)
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}
	if !bytes.Equal(body, payload) {
		t.Fatalf("содержимое не совпало: %q", body)
	}
}

func TestExistsReturnsFalseForMissing(t *testing.T) {
	st := newStorage(t)

	exists, err := st.Exists(context.Background(), "attachments/test/нет-такого-файла")
	if err != nil {
		t.Fatalf("проверка: %v", err)
	}
	if exists {
		t.Fatal("несуществующий объект найден")
	}
}
