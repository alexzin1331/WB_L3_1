package main

import (
	"L3_1/internal/rabbit"
	"L3_1/internal/server"
	"L3_1/internal/storage"
	"L3_1/models"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

const configPath = "config.yaml"

func main() {
	const op = "main"

	// Загружаем конфиг
	cfg := models.MustLoad(configPath)

	// Инициализируем БД
	db, err := storage.New(*cfg)
	if err != nil {
		log.Fatalf("%s: %v", op, err)
	}
	defer db.Close()
	// Инициализируем RabbitMQ
	rabbitClient, err := rabbit.NewRabbit(cfg.RabbitMQ.RabbitMQ)
	if err != nil {
		log.Fatalf("%s: %v", op, err)
	}
	defer rabbitClient.Close()

	uiPath := filepath.Join(".", "ui")
	srv := server.NewServer(db, rabbitClient, uiPath)
	// Канал для ошибок запуска сервераx
	serverErr := make(chan error, 1)

	// Запускаем HTTP-сервер в горутине
	go func() {
		log.Printf("Starting server on %s", cfg.ServConf.Host)
		if err := srv.Start(cfg.ServConf.Host); err != nil {
			serverErr <- fmt.Errorf("server error: %v", err)
		}
	}()

	// Ожидаем сигнал завершения или ошибку сервера
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT)

	select {
	case err := <-serverErr:
		log.Printf("Server failed: %v", err)
	case sig := <-stop:
		log.Printf("Received signal %s, shutting down...", sig)

		if err := srv.Stop(); err != nil {
			log.Printf("Error during shutdown: %v", err)
		}

		log.Println("Server stopped gracefully")
	}
}
