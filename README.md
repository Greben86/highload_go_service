# Highload Go Service with Metrics

Сервис для обработки метрик с аналитикой и мониторингом.

## Быстрый старт

### Локально

```bash
docker compose up -d
```

Проверка работы:
```bash
curl http://localhost:8080/health
```

Остановка:
```bash
docker compose down
```

### В Kubernetes (Minikube)

1. Запустить Minikube:
```bash
minikube start --cpus=2 --memory=4g
minikube addons enable ingress
minikube addons enable metrics-server
```

2. Собрать образ:
```bash
# Linux
eval $(minikube docker-env)
docker build -t go-service:latest .
```

3. Развернуть:
```bash
kubectl apply -f k8s/deploy-all.yaml
kubectl wait --for=condition=ready pod -l app=go-service -n metrics-service --timeout=120s

# Проверить ServiceMonitor
kubectl get servicemonitor -n metrics-service
```

4. Доступ:
```bash
kubectl port-forward -n metrics-service svc/go-service 8080:80
```

## Использование API

Отправить метрику:
```bash
curl -X POST http://localhost:8080/metrics \
  -H "Content-Type: application/json" \
  -d '{"name": "test", "value": 12.3, "timestamp": 1234567890}'
```

Получить аналитику:
```bash
curl http://localhost:8080/analyze
```

## Мониторинг

Установить Prometheus и Grafana:
```bash
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo update
helm install prometheus prometheus-community/kube-prometheus-stack \
  --namespace metrics-service --create-namespace
```

Доступ к Grafana:
```bash
kubectl port-forward -n metrics-service svc/prometheus-grafana 3000:80
```

Доступ к Prometheus:
```bash
kubectl port-forward -n metrics-service svc/prometheus-kube-prometheus-prometheus 9090:9090
```

Открыть `http://localhost:9090` и проверить **Status → Targets** - должен быть target `serviceMonitor/metrics-service/go-service-monitor/0` со статусом **UP**.

Пароль Grafana (Windows):
```powershell
$secret = kubectl get secret --namespace metrics-service prometheus-grafana -o jsonpath="{.data.admin-password}"
[System.Text.Encoding]::UTF8.GetString([System.Convert]::FromBase64String($secret))
```

Пароль Grafana (Linux/Mac):
```bash
kubectl get secret --namespace metrics-service prometheus-grafana \
  -o jsonpath="{.data.admin-password}" | base64 --decode
```

### Импорт дашбордов Grafana

#### Шаг 1: Настройка источника данных

1. В Grafana: Configuration (⚙️) → Data Sources → Add data source
2. Выберите **Prometheus**
3. URL: `http://prometheus-kube-prometheus-prometheus:9090` (внутренний адрес в кластере)
4. Нажмите **Save & Test**

#### Шаг 2: Импорт дашбордов

1. Dashboards → "+" → Import
2. Загрузите JSON файлы из `grafana/dashboards/`:
   - `dashboard-simple.json` - основной дашборд (рекомендуется)
   - `dashboard-rps.json` - производительность
   - `dashboard-anomalies.json` - аномалии

**Как импортировать:**
- Нажмите **"Upload JSON file"** и выберите файл
- Или откройте файл, скопируйте содержимое и вставьте в поле "Import via panel json"
- Если при импорте есть выбор источника данных - выберите Prometheus
- Нажмите **Import**

**Если дашборд пустой после импорта:**
- Создайте дашборд вручную 
- Или откройте настройки дашборда (⚙️) → JSON Model → замените все `"uid": "prometheus"` на UID вашего источника данных

#### Шаг 3: Проверка

- В Dashboards → Browse должны появиться дашборды
- Данные начнут появляться через несколько секунд после отправки метрик
- Отправьте тестовую метрику, чтобы увидеть данные

## Что умеет сервис

- Принимает метрики через API (POST /metrics)
- Кэширует данные в Redis
- Считает аналитику: прогноз (rolling average) и находит аномалии (z-score)
- Отдает метрики для Prometheus
- Автоматически масштабируется в Kubernetes (HPA)

## Структура проекта

```
.
├── main.go       # Основной код
├── compose.yml   # Локальный запуск
├── Dockerfile    # Образ для Kubernetes
├── k8s/          # Kubernetes манифесты
└── jmeter/       # Нагрузочное тестирование
```
