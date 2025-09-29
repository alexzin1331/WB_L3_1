package storage

import (
	"L3_1/models"
	"context"
	"fmt"
	"net/url"
	"strconv"
	"testing"
	"time"

	"database/sql"

	"github.com/golang-migrate/migrate/v4"
	p "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	r "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"
)

type TestDependencies struct {
	PostgresContainer *postgres.PostgresContainer
	RedisContainer    *redis.RedisContainer
	Storage           *Storage
}

func SetupTestDependencies(ctx context.Context) (*TestDependencies, error) {
	// Setup PostgreSQL container
	pgContainer, err := postgres.Run(ctx,
		"postgres:15-alpine",
		postgres.WithDatabase("test_db"),
		postgres.WithUsername("test_user"),
		postgres.WithPassword("test_password"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second)),
	)
	if err != nil {
		return nil, err
	}

	// Setup Redis container
	redisContainer, err := redis.Run(ctx,
		"redis:7-alpine",
		testcontainers.WithWaitStrategy(
			wait.ForLog("Ready to accept connections").
				WithStartupTimeout(30*time.Second)),
	)
	if err != nil {
		return nil, err
	}

	// Get connection strings
	pgConnStr, err := pgContainer.ConnectionString(ctx)
	if err != nil {
		return nil, err
	}

	redisConnStr, err := redisContainer.ConnectionString(ctx)
	if err != nil {
		return nil, err
	}

	// Правильно формируем строку подключения с sslmode=disable
	// Парсим существующую строку подключения и добавляем параметр
	connURL, err := url.Parse(pgConnStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse connection string: %v", err)
	}

	query := connURL.Query()
	query.Set("sslmode", "disable")
	connURL.RawQuery = query.Encode()

	connString := connURL.String()

	// Initialize database connection for migrations
	db, err := sql.Open("pgx", connString)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	// Run migrations
	if err := runTestMigrations(db); err != nil {
		return nil, err
	}

	// Create storage with testcontainers connection strings
	poolConfig, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, err
	}

	pgxPool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, err
	}

	// Initialize Redis client
	rdb := r.NewClient(&r.Options{
		Addr: redisConnStr[8:],
	})

	// Wait for Redis to be ready
	_, err = rdb.Ping(ctx).Result()
	if err != nil {
		return nil, err
	}

	storage := &Storage{
		db:    pgxPool,
		cache: rdb,
	}

	return &TestDependencies{
		PostgresContainer: pgContainer,
		RedisContainer:    redisContainer,
		Storage:           storage,
	}, nil
}

func (td *TestDependencies) Cleanup(ctx context.Context) {
	if td.Storage != nil {
		td.Storage.Close()
	}
	if td.RedisContainer != nil {
		td.RedisContainer.Terminate(ctx)
	}
	if td.PostgresContainer != nil {
		td.PostgresContainer.Terminate(ctx)
	}
}

func runTestMigrations(db *sql.DB) error {
	driver, err := p.WithInstance(db, &p.Config{})
	if err != nil {
		return err
	}

	m, err := migrate.NewWithDatabaseInstance(
		"file://../../migrations",
		"postgres",
		driver,
	)
	if err != nil {
		return err
	}

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return err
	}

	return nil
}

func TestStorage_CreateAndGet(t *testing.T) {
	ctx := context.Background()

	// Setup test dependencies
	deps, err := SetupTestDependencies(ctx)
	require.NoError(t, err)
	defer deps.Cleanup(ctx)

	storage := deps.Storage

	lastError := ""

	// Test data
	notification := &models.Notification{
		UserID:        strconv.Itoa(123),
		DueAt:         time.Now().Add(1 * time.Hour),
		Message:       "Test notification",
		Status:        "scheduled",
		AttemptCount:  0,
		MaxAttempts:   3,
		NextAttemptAt: time.Now().Add(1 * time.Hour),
		LastError:     &lastError,
	}

	// Test Create
	err = storage.Create(ctx, notification)
	require.NoError(t, err)
	assert.NotZero(t, notification.ID)

	// Test GetByID
	retrieved, err := storage.GetByID(ctx, notification.ID)
	require.NoError(t, err)

	assert.Equal(t, notification.ID, retrieved.ID)
	assert.Equal(t, notification.UserID, retrieved.UserID)
	assert.Equal(t, notification.Message, retrieved.Message)
	assert.Equal(t, notification.Status, retrieved.Status)
	assert.Equal(t, notification.AttemptCount, retrieved.AttemptCount)
	assert.Equal(t, notification.MaxAttempts, retrieved.MaxAttempts)
}

func TestStorage_Update(t *testing.T) {
	ctx := context.Background()

	deps, err := SetupTestDependencies(ctx)
	require.NoError(t, err)
	defer deps.Cleanup(ctx)

	storage := deps.Storage

	lastError := ""

	// Create a notification first
	notification := &models.Notification{
		UserID:        strconv.Itoa(456),
		DueAt:         time.Now().Add(1 * time.Hour),
		Message:       "Test notification for update",
		Status:        "scheduled",
		AttemptCount:  0,
		MaxAttempts:   3,
		NextAttemptAt: time.Now().Add(1 * time.Hour),
		LastError:     &lastError,
	}

	err = storage.Create(ctx, notification)
	require.NoError(t, err)
	noError := "No error"
	// Update the notification
	notification.Status = "sent"
	notification.AttemptCount = 1
	notification.LastError = &noError

	err = storage.Update(ctx, *notification)
	require.NoError(t, err)

	// Verify update
	updated, err := storage.GetByID(ctx, notification.ID)
	require.NoError(t, err)

	assert.Equal(t, "sent", updated.Status)
	assert.Equal(t, 1, updated.AttemptCount)
	assert.Equal(t, "No error", *updated.LastError)
}

func TestStorage_CacheIntegration(t *testing.T) {
	ctx := context.Background()

	deps, err := SetupTestDependencies(ctx)
	require.NoError(t, err)
	defer deps.Cleanup(ctx)

	storage := deps.Storage

	lastError := ""

	// Create a notification
	notification := &models.Notification{
		UserID:        strconv.Itoa(789),
		DueAt:         time.Now().Add(1 * time.Hour),
		Message:       "Test cache notification",
		Status:        "scheduled",
		AttemptCount:  0,
		MaxAttempts:   3,
		NextAttemptAt: time.Now().Add(1 * time.Hour),
		LastError:     &lastError,
	}

	err = storage.Create(ctx, notification)
	require.NoError(t, err)

	// First call - should go to database and populate cache
	firstCall, err := storage.GetByID(ctx, notification.ID)
	require.NoError(t, err)
	assert.Equal(t, notification.Message, firstCall.Message)

	// Second call - should come from cache
	secondCall, err := storage.GetByID(ctx, notification.ID)
	require.NoError(t, err)
	assert.Equal(t, notification.Message, secondCall.Message)

	// Verify it's the same data
	assert.Equal(t, firstCall, secondCall)
}

func TestStorage_Delete(t *testing.T) {
	ctx := context.Background()

	deps, err := SetupTestDependencies(ctx)
	require.NoError(t, err)
	defer deps.Cleanup(ctx)

	storage := deps.Storage

	lastError := ""

	// Create a notification
	notification := &models.Notification{
		UserID:        strconv.Itoa(999),
		DueAt:         time.Now().Add(1 * time.Hour),
		Message:       "Test delete notification",
		Status:        "scheduled",
		AttemptCount:  0,
		MaxAttempts:   3,
		NextAttemptAt: time.Now().Add(1 * time.Hour),
		LastError:     &lastError,
	}

	err = storage.Create(ctx, notification)
	require.NoError(t, err)

	// Verify it exists
	_, err = storage.GetByID(ctx, notification.ID)
	require.NoError(t, err)

	// Delete the notification
	err = storage.Delete(ctx, notification.ID)
	require.NoError(t, err)

	// Verify it's deleted by trying to get it (should return error)
	_, err = storage.GetByID(ctx, notification.ID)
	assert.Error(t, err)
}

func TestStorage_GetByID_NotFound(t *testing.T) {
	ctx := context.Background()

	deps, err := SetupTestDependencies(ctx)
	require.NoError(t, err)
	defer deps.Cleanup(ctx)

	storage := deps.Storage

	// Try to get non-existent notification
	_, err = storage.GetByID(ctx, 99999)
	assert.Error(t, err)
}

func TestStorage_ConcurrentOperations(t *testing.T) {
	ctx := context.Background()

	deps, err := SetupTestDependencies(ctx)
	require.NoError(t, err)
	defer deps.Cleanup(ctx)

	storage := deps.Storage

	// Create multiple notifications concurrently
	const numNotifications = 5
	notifications := make([]*models.Notification, numNotifications)

	lastError := ""

	for i := 0; i < numNotifications; i++ {
		notification := &models.Notification{
			UserID:        strconv.Itoa(1000 + i),
			DueAt:         time.Now().Add(time.Duration(i) * time.Hour),
			Message:       fmt.Sprintf("Concurrent notification %d", i),
			Status:        "scheduled",
			AttemptCount:  0,
			MaxAttempts:   3,
			NextAttemptAt: time.Now().Add(time.Duration(i) * time.Hour),
			LastError:     &lastError,
		}
		notifications[i] = notification
	}

	// Create all notifications
	for i, notification := range notifications {
		err := storage.Create(ctx, notification)
		require.NoError(t, err, "Failed to create notification %d", i)
		assert.NotZero(t, notification.ID, "Notification ID should not be zero for notification %d", i)
	}

	// Verify all notifications can be retrieved
	for i, notification := range notifications {
		retrieved, err := storage.GetByID(ctx, notification.ID)
		require.NoError(t, err, "Failed to get notification %d", i)
		assert.Equal(t, notification.Message, retrieved.Message, "Message mismatch for notification %d", i)
	}
}
