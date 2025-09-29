package server

import (
	"L3_1/models"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"strconv"
	"time"

	"github.com/buaazp/fasthttprouter"
	"github.com/valyala/fasthttp"
)

type StorageClient interface {
	Create(ctx context.Context, n *models.Notification) error
	GetByID(ctx context.Context, id int64) (models.Notification, error)
	Delete(ctx context.Context, id int64) error
	Update(ctx context.Context, n models.Notification) error
	Close()
}

type RabbitMQClient interface {
	PublishNotification(ctx context.Context, notification models.Notification) error
	StartProcessing(handler func(notification models.Notification) error) error
	Close() error
}

type Server struct {
	storage    StorageClient
	router     *fasthttprouter.Router
	rabbit     RabbitMQClient
	httpServer *fasthttp.Server
	stopChan   chan struct{}
	uiPath     string
}

func NewServer(storage StorageClient, rabbit RabbitMQClient, uiPath string) *Server {
	s := &Server{
		storage:  storage,
		rabbit:   rabbit,
		stopChan: make(chan struct{}),
		router:   fasthttprouter.New(),
		uiPath:   uiPath,
	}

	s.router.POST("/notify", s.createNotification)
	s.router.GET("/notify/:id", s.getNotification)
	s.router.DELETE("/notify/:id", s.deleteNotification)

	// Добавьте обработчик для UI
	s.router.GET("/", s.serveUI)
	s.router.GET("/index.html", s.serveUI)
	s.router.ServeFiles("/static/*filepath", s.uiPath)

	s.httpServer = &fasthttp.Server{
		Handler:            s.router.Handler,
		ReadTimeout:        10 * time.Second,
		WriteTimeout:       10 * time.Second,
		MaxRequestBodySize: 1024 * 1024,
		IdleTimeout:        30 * time.Second,
	}
	return s
}

func (s *Server) serveUI(ctx *fasthttp.RequestCtx) {
	indexPath := filepath.Join(s.uiPath, "index.html")

	ctx.SetContentType("text/html")
	fasthttp.ServeFile(ctx, indexPath)
}

func (s *Server) Start(addr string) error {
	if err := s.rabbit.StartProcessing(s.handleNotification); err != nil {
		log.Fatalf("can't start processing message: %v", err)
	}

	log.Printf("Starting server on %s", addr)
	return s.httpServer.ListenAndServe(addr)
}

func (s *Server) Stop() error {
	close(s.stopChan)

	if err := s.httpServer.Shutdown(); err != nil {
		log.Printf("HTTP server shutdown error: %v", err)
	}

	if err := s.rabbit.Close(); err != nil {
		log.Printf("RabbitMQ close error: %v", err)
	}

	s.storage.Close()
	return nil
}

func (s *Server) createNotification(ctx *fasthttp.RequestCtx) {
	var req struct {
		UserID      string    `json:"user_id"`
		DueAt       time.Time `json:"due_at"`
		Message     string    `json:"message"`
		MaxAttempts int       `json:"max_attempts"`
	}

	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		log.Printf("Failed to unmarshal request: %v", err)
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
		ctx.SetContentType("application/json")
		fmt.Fprintf(ctx, `{"error": "Invalid JSON format"}`)
		return
	}

	// Валидация входных данных
	if req.UserID == "" {
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
		ctx.SetContentType("application/json")
		fmt.Fprintf(ctx, `{"error": "UserID is required"}`)
		return
	}

	if req.Message == "" {
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
		ctx.SetContentType("application/json")
		fmt.Fprintf(ctx, `{"error": "Message is required"}`)
		return
	}

	if req.DueAt.IsZero() {
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
		ctx.SetContentType("application/json")
		fmt.Fprintf(ctx, `{"error": "DueAt is required"}`)
		return
	}

	if req.DueAt.Before(time.Now()) {
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
		ctx.SetContentType("application/json")
		fmt.Fprintf(ctx, `{"error": "DueAt must be in the future"}`)
		return
	}

	if req.MaxAttempts <= 0 {
		req.MaxAttempts = 3
	}

	notification := models.Notification{
		UserID:       req.UserID,
		DueAt:        req.DueAt,
		Message:      req.Message,
		Status:       models.StatusScheduled,
		MaxAttempts:  req.MaxAttempts,
		AttemptCount: 0,
	}

	// Используем контекст запроса вместо Background()
	if err := s.storage.Create(ctx, &notification); err != nil {
		log.Printf("Failed to create notification: %v", err)
		ctx.SetStatusCode(fasthttp.StatusInternalServerError)
		ctx.SetContentType("application/json")
		fmt.Fprintf(ctx, `{"error": "Failed to create notification"}`)
		return
	}

	if err := s.rabbit.PublishNotification(ctx, notification); err != nil {
		log.Printf("Failed to publish notification: %v", err)
		ctx.SetStatusCode(fasthttp.StatusInternalServerError)
		ctx.SetContentType("application/json")
		fmt.Fprintf(ctx, `{"error": "Failed to schedule notification"}`)
		return
	}

	ctx.SetStatusCode(fasthttp.StatusCreated)
	ctx.SetContentType("application/json")
	if resp, err := json.Marshal(notification); err == nil {
		ctx.SetBody(resp)
	} else {
		log.Printf("Failed to marshal response: %v", err)
		ctx.SetStatusCode(fasthttp.StatusInternalServerError)
		fmt.Fprintf(ctx, `{"error": "Failed to create response"}`)
	}
}

func (s *Server) getNotification(ctx *fasthttp.RequestCtx) {
	idStr := ctx.UserValue("id").(string)
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
		ctx.SetContentType("application/json")
		fmt.Fprintf(ctx, `{"error": "Invalid ID format"}`)
		return
	}

	notification, err := s.storage.GetByID(ctx, id)
	if err != nil {
		log.Printf("Failed to get notification %d: %v", id, err)
		ctx.SetStatusCode(fasthttp.StatusNotFound)
		ctx.SetContentType("application/json")
		fmt.Fprintf(ctx, `{"error": "Notification not found"}`)
		return
	}

	ctx.SetContentType("application/json")
	if resp, err := json.Marshal(notification); err == nil {
		ctx.SetBody(resp)
	} else {
		log.Printf("Failed to marshal notification: %v", err)
		ctx.SetStatusCode(fasthttp.StatusInternalServerError)
		fmt.Fprintf(ctx, `{"error": "Failed to get notification"}`)
	}
}

func (s *Server) deleteNotification(ctx *fasthttp.RequestCtx) {
	idStr := ctx.UserValue("id").(string)
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
		ctx.SetContentType("application/json")
		fmt.Fprintf(ctx, `{"error": "Invalid ID format"}`)
		return
	}

	if err := s.storage.Delete(ctx, id); err != nil {
		log.Printf("Failed to delete notification %d: %v", id, err)
		ctx.SetStatusCode(fasthttp.StatusInternalServerError)
		ctx.SetContentType("application/json")
		fmt.Fprintf(ctx, `{"error": "Failed to delete notification"}`)
		return
	}

	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetContentType("application/json")
	fmt.Fprintf(ctx, `{"message": "Notification deleted successfully"}`)
}

func (s *Server) handleNotification(notification models.Notification) error {
	log.Printf("Processing notification %d for user %s", notification.ID, notification.UserID)

	// Простая имитация обработки - всегда успешно
	time.Sleep(100 * time.Millisecond)
	log.Printf("Notification %d sent successfully to user %s", notification.ID, notification.UserID)

	// Обновляем статус в БД на "sent"
	ctx := context.Background()
	currentNotification, err := s.storage.GetByID(ctx, notification.ID)
	if err != nil {
		log.Printf("Failed to get notification %d: %v", notification.ID, err)
		return err
	}

	currentNotification.Status = models.StatusSent
	currentNotification.AttemptCount++

	// Добавим метод Update в storage (см. ниже)
	if err := s.storage.Update(ctx, currentNotification); err != nil {
		log.Printf("Failed to update notification %d: %v", notification.ID, err)
	}

	return nil
}
