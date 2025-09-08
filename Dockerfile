FROM golang:1.24-alpine AS builder

WORKDIR /app

# Копируем файлы модулей и загружаем зависимости
COPY go.mod go.sum ./
RUN go mod download

# Копируем исходный код
COPY . .

# Собираем приложение
RUN CGO_ENABLED=0 GOOS=linux go build -o /app/bin/notify-service ./cmd

FROM alpine:latest

WORKDIR /app

# Копируем бинарник из builder stage
COPY --from=builder /app/bin/notify-service .
COPY config.yaml .
COPY migrations ./migrations
COPY ui ./ui

# Устанавливаем зависимости для миграций
RUN apk add --no-cache postgresql-client

# Создаем не-root пользователя для безопасности
RUN adduser -D -g '' appuser
USER appuser

EXPOSE 8081

CMD ["./notify-service"]