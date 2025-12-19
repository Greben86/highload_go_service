package main

import (
	"context"
	"fmt"
	"log"
	"math/rand/v2"
	"strconv"

	// "math"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	ctx = context.Background()

	// Prometheus metrics
	rpsCounter = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rps_total",
		},
		[]string{"status"},
	)

	latencyHistogram = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "latency_seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"endpoint"},
	)

	anomalyCounter = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "anomalies_total",
		},
	)

	predictionGauge = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "prediction_value",
		},
	)
)

type Service struct {
	mutex     sync.RWMutex
	redis     *redis.Client
	analytics *Analytics
}

func NewService(redisAddr string, password string) (*Service, error) {
	redis := redis.NewClient(&redis.Options{
		Addr:         redisAddr,
		Password:     password,
		DB:           0,
		PoolSize:     10,
		MinIdleConns: 5,
	})

	if err := redis.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("Failed to connect to Redis: %w", err)
	}

	return &Service{
		redis: redis,
		analytics: &Analytics{
			totalCount:   0,
			anomalyCount: 0,
			prediction:   0,
		},
	}, nil
}

func (service *Service) predictFromSuperAI() float64 {
	return rand.Float64()
}

// Функция для расчёта скользящего среднего (moving average)
func (service *Service) rollingAverage(metricName string, newValue float64, windowSize int64) float64 {
	key := fmt.Sprintf("%s:rolling", metricName)
	vals, err := service.redis.LRange(ctx, key, 0, -1).Result()
	if err != nil && err != redis.Nil {
		log.Println(err)
		return newValue
	}

	var sum float64
	for _, v := range vals {
		fv, _ := strconv.ParseFloat(v, 64)
		sum += fv
	}

	newSum := sum + newValue
	service.redis.RPush(ctx, key, newValue)
	service.redis.LTrim(ctx, key, -(windowSize + 1), -1)
	var predict = service.predictFromSuperAI()
	service.analytics.prediction = predict
	predictionGauge.Set(predict)

	return newSum / float64(len(vals)+1)
}

// Функция для вычисления отклонения (z-score) и детекции аномалий
func (service *Service) detectAnomaly(metricName string, newValue float64) bool {
	keyMean := fmt.Sprintf("%s:mean", metricName)
	keyStdDev := fmt.Sprintf("%s:stddev", metricName)

	meanStr, _ := service.redis.Get(ctx, keyMean).Result()
	stdDevStr, err := service.redis.Get(ctx, keyStdDev).Result()

	if err != nil || meanStr == "" || stdDevStr == "" {
		return false // Недостаточно данных для анализа
	}

	mean, _ := strconv.ParseFloat(meanStr, 64)
	stdDev, _ := strconv.ParseFloat(stdDevStr, 64)
	zScore := (newValue - mean) / stdDev

	return service.abs(zScore) > 2 // Если z-score превышает 2, считаем это аномалией
}

// Вспомогательная функция для нахождения модуля числа
func (service *Service) abs(x float64) float64 {
	if x >= 0 {
		return x
	}
	return -x
}

// Маршрутизатор для обработки входящих метрик
func (service *Service) handleMetrics(c *gin.Context) {
	var m MetricDto
	if err := c.ShouldBindJSON(&m); err != nil { // Привязываем тело запроса к структуре Metric
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		go rpsCounter.WithLabelValues("error").Inc()
		return
	}

	// Проверяем наличие аномалий
	go func() {
		// Сохраняем исходное значение в Redis
		valueKey := fmt.Sprintf("%s:%d", m.Name, m.Timestamp)
		service.redis.Set(ctx, valueKey, m.Value, time.Hour*24)

		// Рассчитываем rolling average
		var windowSize int64 = 10
		avg := service.rollingAverage(m.Name, m.Value, windowSize)
		fmt.Printf("Rolling avg for %s at timestamp %d is %.2f\n", m.Name, m.Timestamp, avg)

		isAnomaly := service.detectAnomaly(m.Name, m.Value)
		if isAnomaly {
			service.analytics.anomalyCount++
			anomalyCounter.Inc()
			log.Printf("ANOMALY DETECTED: Value %f exceeds normal distribution bounds!\n", m.Value)
		}
	}()

	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("Received metric '%s' with value %.2f", m.Name, m.Value)})
	go rpsCounter.WithLabelValues("success").Inc()
	service.analytics.totalCount++
}

func (service *Service) handleAnalytics(c *gin.Context) {
	start := time.Now()
	service.mutex.RLock()
	defer service.mutex.RUnlock()

	response := gin.H{
		"prediction":    service.analytics.prediction,
		"window_size":   50,
		"total_count":   service.analytics.totalCount,
		"anomaly_count": service.analytics.anomalyCount,
	}

	latencyHistogram.WithLabelValues("analyze").Observe(time.Since(start).Seconds())
	c.JSON(http.StatusOK, response)
}

func (service *Service) handleHealth(c *gin.Context) {
	if err := service.redis.Ping(ctx).Err(); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"status": "DOWN",
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"status": "UP",
	})
}

func main() {
	redisAddr := GetEnv("REDIS_ADDR", "localhost:6379")
	redisPass := GetEnv("REDIS_PASS", "")

	service, err := NewService(redisAddr, redisPass)
	if err != nil {
		log.Fatalf("Failed to initialize service: %v", err)
	}

	route := gin.Default()

	route.GET("/health", service.handleHealth)
	route.POST("/metrics", service.handleMetrics)
	route.GET("/analyze", service.handleAnalytics)
	route.GET("/prometheus", gin.WrapH(promhttp.Handler()))

	route.Run()
}
