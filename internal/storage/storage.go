// Package storage — S3-совместимое хранилище файлов.
//
// Код написан под стандартный S3 API (minio-go работает с любым совместимым
// сервером). Поэтому SeaweedFS локально, Cloudflare R2 или Backblaze B2 в
// проде — это разница в адресе и ключах, а не в коде.
package storage

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

const (
	// UploadTTL — сколько живёт ссылка на загрузку. Клиент должен успеть
	// начать загрузку; сама загрузка может идти дольше.
	UploadTTL = 15 * time.Minute

	// DownloadTTL — срок жизни ссылки на скачивание.
	DownloadTTL = time.Hour
)

type Config struct {
	Endpoint  string // host:port без схемы, например localhost:8333
	AccessKey string
	SecretKey string
	Bucket    string
	UseSSL    bool
}

type Storage struct {
	client *minio.Client
	bucket string
}

func New(ctx context.Context, cfg Config) (*Storage, error) {
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("клиент хранилища: %w", err)
	}

	exists, err := client.BucketExists(ctx, cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("проверка бакета: %w", err)
	}

	if !exists {
		if err := client.MakeBucket(ctx, cfg.Bucket, minio.MakeBucketOptions{}); err != nil {
			// Гонка: другой экземпляр создал бакет между проверкой и созданием.
			// Это не ошибка — бакет есть, работать можно.
			resp := minio.ToErrorResponse(err)
			if resp.Code != "BucketAlreadyOwnedByYou" && resp.Code != "BucketAlreadyExists" {
				return nil, fmt.Errorf("создание бакета: %w", err)
			}
		}
	}

	return &Storage{client: client, bucket: cfg.Bucket}, nil
}

// PresignedPut — ссылка, по которой клиент сам загрузит файл методом PUT.
// Файл не проходит через наш сервер: не занимает память и канал.
//
// Content-Type в подпись не включаем: minio-go подписывает только путь,
// а клиент может отправить любой заголовок. Тип проверяем при привязке
// файла к сообщению.
func (s *Storage) PresignedPut(ctx context.Context, key string) (string, error) {
	u, err := s.client.PresignedPutObject(ctx, s.bucket, key, UploadTTL)
	if err != nil {
		return "", fmt.Errorf("ссылка на загрузку: %w", err)
	}
	return u.String(), nil
}

// PresignedGet — ссылка на скачивание.
func (s *Storage) PresignedGet(ctx context.Context, key string) (string, error) {
	u, err := s.client.PresignedGetObject(ctx, s.bucket, key, DownloadTTL, url.Values{})
	if err != nil {
		return "", fmt.Errorf("ссылка на скачивание: %w", err)
	}
	return u.String(), nil
}

// Exists проверяет, что объект действительно загружен.
// Нужен, чтобы нельзя было прикрепить к сообщению несуществующий ключ.
func (s *Storage) Exists(ctx context.Context, key string) (bool, error) {
	_, exists, err := s.Stat(ctx, key)
	return exists, err
}

// Stat возвращает размер объекта и признак существования.
// Размер берём из хранилища, а не из запроса клиента: клиент может соврать.
func (s *Storage) Stat(ctx context.Context, key string) (int64, bool, error) {
	info, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		resp := minio.ToErrorResponse(err)
		if resp.Code == "NoSuchKey" || resp.StatusCode == 404 {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("проверка объекта: %w", err)
	}
	return info.Size, true, nil
}
