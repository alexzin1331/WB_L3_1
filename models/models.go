package models

import (
	"github.com/ilyakaznacheev/cleanenv"
	"log"
	"time"
)

const (
	StatusScheduled = "scheduled"
	StatusSent      = "sent"
)

type Notification struct {
	ID            int64     `json:"id"`
	UserID        string    `json:"user_id"`
	DueAt         time.Time `json:"due_at"`
	Message       string    `json:"message"`
	Status        string    `json:"status"`
	AttemptCount  int       `json:"attempt_count"`
	MaxAttempts   int       `json:"max_attempts"`
	NextAttemptAt time.Time `json:"next_attempt_at"`
	LastError     *string   `json:"last_error"`
	CreatedAt     time.Time `json:"created_at"`
}

type Config struct {
	ServConf ServerCfg   `yaml:"server"`
	DBConf   DatabaseCfg `yaml:"database"`
	RDBConf  Redis       `yaml:"redis"`
	RabbitMQ Rabbit      `yaml:"rabbitmq"`
}

type Redis struct {
	RedisAddress  string `yaml:"redis_address"`
	RedisPassword string `yaml:"redis_password"`
	RedisDB       int    `yaml:"redis_db"`
}

type ServerCfg struct {
	Timeout time.Duration `yaml:"timeout" env:"TIMEOUT" env-default:"10s"`
	Host    string        `yaml:"hostGateway" env:"HostGateway" env-default:":8081"`
}

type Rabbit struct {
	RabbitMQ string `yaml:"URL" env:"RabbitMQ" env-default:"amqp://guest:guest@rabbitmq:5672/"`
}

type DatabaseCfg struct {
	Port     string `yaml:"port" env:"DB_PORT" env-default:"5432"`
	User     string `yaml:"user" env:"DB_USER" env-default:"postgres"`
	Password string `yaml:"password" env:"DB_PASSWORD" env-default:"1234"`
	DBName   string `yaml:"dbname" env:"DB_NAME" env-default:"postgres"`
	Host     string `yaml:"host" env:"DB_HOST" env-default:"localhost"`
}

func MustLoad(path string) *Config {
	conf := &Config{}
	if err := cleanenv.ReadConfig(path, conf); err != nil {
		log.Fatal("Can't read the common config")
		return nil
	}
	return conf
}
