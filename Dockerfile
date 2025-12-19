# Multi-stage build для минимального размера образа
FROM golang:1.24-alpine AS builder

# Копирование файлов
COPY . /app

# Установка рабочей директории
WORKDIR /app

# Загрузка зависимостей
RUN go mod download

# Собираем приложение
RUN go build -o app .

# Финальный образ
FROM golang:1.24-alpine AS runner

RUN apk --no-cache add ca-certificates

WORKDIR /root/

# Копируем бинарник из builder
COPY --from=builder /app/app .

EXPOSE 8080

CMD ["./app"]