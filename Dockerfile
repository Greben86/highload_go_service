FROM golang:1.24-alpine

# Копирование файлов
COPY . /app

# Установка рабочей директории
WORKDIR /app

# Загрузка зависимостей
RUN go mod download

# Сборка приложения
RUN go build -o app .

# Открытие порта
EXPOSE 8080

ENTRYPOINT ["./app"]