package storage

import (
	"L3_1/models"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	_ "github.com/jackc/pgx/v5/stdlib"
	"log"
	"strconv"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

const (
	migrationPath = "file://migrations"
	cacheDuration = 10 * time.Minute
)

type Storage struct {
	db    *pgxpool.Pool
	cache *redis.Client
}

func New(c models.Config) (*Storage, error) {
	const op = "storage.New"

	// Инициализация Redis
	cache, err := initRedis(c)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", op, err)
	}

	// Правильная строка подключения
	connString := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable",
		c.DBConf.User, c.DBConf.Password, c.DBConf.Host, c.DBConf.Port, c.DBConf.DBName)

	poolConfig, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, fmt.Errorf("%s: failed to parse connection string: %v", op, err)
	}

	db, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
	if err != nil {
		return nil, fmt.Errorf("%s: failed to create connection pool: %v", op, err)
	}

	// Проверка соединения
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := db.Ping(ctx); err != nil {
		return nil, fmt.Errorf("%s: failed to ping database: %v", op, err)
	}

	// Миграции
	sqlDB, err := sql.Open("pgx", connString)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("%s: failed to open sql.DB for migrations: %v", op, err)
	}
	defer sqlDB.Close()

	if err := runMigrations(sqlDB); err != nil {
		db.Close()
		return nil, fmt.Errorf("%s: failed to run migrations: %v", op, err)
	}

	log.Println("Storage initialized successfully")
	return &Storage{db: db, cache: cache}, nil
}

func initRedis(config models.Config) (*redis.Client, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:     config.RDBConf.RedisAddress,
		Password: config.RDBConf.RedisPassword,
		DB:       config.RDBConf.RedisDB,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := rdb.Ping(ctx).Result(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis: %v", err)
	}
	return rdb, nil
}

func runMigrations(db *sql.DB) error {
	const op = "storage.runMigrations"

	driver, err := postgres.WithInstance(db, &postgres.Config{})
	if err != nil {
		return fmt.Errorf("%s: %v", op, err)
	}

	m, err := migrate.NewWithDatabaseInstance(migrationPath, "postgres", driver)
	if err != nil {
		return fmt.Errorf("%s: %v", op, err)
	}

	if version, dirty, err := m.Version(); err == nil && dirty {
		log.Printf("%s: dirty database at version %d, forcing...", op, version)
		if err := m.Force(int(version)); err != nil {
			return fmt.Errorf("%s: failed to force version: %v", op, err)
		}
	}

	if err = m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("%s: %v", op, err)
	}

	log.Println("Database migrations applied successfully")
	return nil
}

func (s *Storage) Create(ctx context.Context, n *models.Notification) error {
	const op = "storage.Create"

	query := `
		INSERT INTO notifications 
		(user_id, due_at, message, status, attempt_count, max_attempts, next_attempt_at, last_error)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id`

	err := s.db.QueryRow(ctx, query,
		n.UserID,
		n.DueAt,
		n.Message,
		n.Status,
		n.AttemptCount,
		n.MaxAttempts,
		n.NextAttemptAt,
		n.LastError,
	).Scan(&n.ID)

	if err != nil {
		return fmt.Errorf("%s: %v", op, err)
	}
	return nil
}

func (s *Storage) GetByID(ctx context.Context, id int64) (models.Notification, error) {
	const op = "storage.GetByID"

	// Пробуем получить из кэша
	cacheKey := strconv.FormatInt(id, 10)
	if data, err := s.cache.Get(ctx, cacheKey).Result(); err == nil {
		var notification models.Notification
		if err := json.Unmarshal([]byte(data), &notification); err == nil {
			return notification, nil
		}
		log.Printf("%s: failed to unmarshal cached notification: %v", op, err)
	}

	// Получаем из базы
	query := `
		SELECT id, user_id, due_at, message, status, attempt_count, max_attempts, 
		       next_attempt_at, last_error
		FROM notifications 
		WHERE id = $1`

	var n models.Notification
	err := s.db.QueryRow(ctx, query, id).Scan(
		&n.ID,
		&n.UserID,
		&n.DueAt,
		&n.Message,
		&n.Status,
		&n.AttemptCount,
		&n.MaxAttempts,
		&n.NextAttemptAt,
		&n.LastError,
	)

	if err != nil {
		return models.Notification{}, fmt.Errorf("%s: %v", op, err)
	}

	// Сохраняем в кэш
	if data, err := json.Marshal(n); err == nil {
		s.cache.Set(ctx, cacheKey, data, cacheDuration)
	} else {
		log.Printf("%s: failed to marshal notification: %v", op, err)
	}

	return n, nil
}

func (s *Storage) Delete(ctx context.Context, id int64) error {
	const op = "storage.Delete"

	// Меняем статус вместо удаления
	query := `DELETE FROM notifications WHERE id = $1`
	_, err := s.db.Exec(ctx, query, id)
	if err != nil {
		return fmt.Errorf("%s: %v", op, err)
	}

	// Инвалидируем кэш
	s.cache.Del(ctx, strconv.FormatInt(id, 10))
	return nil
}

func (s *Storage) Update(ctx context.Context, n models.Notification) error {
	const op = "storage.Update"
	query := `
        UPDATE notifications 
        SET status = $1, attempt_count = $2, last_error = $3
        WHERE id = $4`

	_, err := s.db.Exec(ctx, query, n.Status, n.AttemptCount, n.LastError, n.ID)
	if err != nil {
		return fmt.Errorf("%s: %v", op, err)
	}

	// Инвалидируем кэш
	s.cache.Del(ctx, strconv.FormatInt(n.ID, 10))
	return nil
}

func (s *Storage) Close() {
	if s.db != nil {
		s.db.Close()
	}
	if s.cache != nil {
		s.cache.Close()
	}
}
