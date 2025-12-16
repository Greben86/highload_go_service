package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	windowSize      = 50
	zScoreThreshold = 2.0
	redisKeyPrefix  = "metric:"
)

var (
	ctx = context.Background()

	// Prometheus metrics
	rpsCounter = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "service_rps_total",
			Help: "Total requests per second",
		},
		[]string{"status"},
	)

	latencyHistogram = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "service_latency_seconds",
			Help:    "Request latency in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"endpoint"},
	)

	anomalyCounter = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "service_anomalies_total",
			Help: "Total number of detected anomalies",
		},
	)

	anomalyRateGauge = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "service_anomaly_rate",
			Help: "Current anomaly rate",
		},
	)

	predictionGauge = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "service_prediction_value",
			Help: "Predicted value using rolling average",
		},
	)
)

type Metric struct {
	Timestamp int64   `json:"timestamp"` // Unix timestamp in seconds
	CPU       float64 `json:"cpu"`
	RPS       float64 `json:"rps"`
}

type Analytics struct {
	mu              sync.RWMutex
	rollingWindow   []float64
	anomalyCount    int64
	totalMetrics    int64
	prediction      float64
	mean            float64
	stdDev          float64
	anomalyRate     float64
}

type Service struct {
	redis    *redis.Client
	analytics *Analytics
}

func NewService(redisAddr string) (*Service, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:         redisAddr,
		Password:     "",
		DB:           0,
		PoolSize:     10,
		MinIdleConns: 5,
	})

	// Test connection
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis: %w", err)
	}

	return &Service{
		redis: rdb,
		analytics: &Analytics{
			rollingWindow: make([]float64, 0, windowSize),
		},
	}, nil
}

func (s *Service) ingestMetric(c *gin.Context) {
	start := time.Now()
	var metric Metric

	if err := c.ShouldBindJSON(&metric); err != nil {
		rpsCounter.WithLabelValues("error").Inc()
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// If timestamp is 0 or not provided, use current time
	if metric.Timestamp == 0 {
		metric.Timestamp = time.Now().Unix()
	}

	// Store in Redis with expiration
	key := fmt.Sprintf("%s%d", redisKeyPrefix, metric.Timestamp)
	data, _ := json.Marshal(metric)
	if err := s.redis.Set(ctx, key, data, 10*time.Minute).Err(); err != nil {
		log.Printf("Failed to cache metric: %v", err)
	}

	// Process metric through analytics in background
	go s.processMetricValue(metric.RPS)

	rpsCounter.WithLabelValues("success").Inc()
	latencyHistogram.WithLabelValues("ingest").Observe(time.Since(start).Seconds())

	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"message": "Metric ingested successfully",
	})
}

func (s *Service) processMetricValue(rps float64) {
	s.analytics.mu.Lock()
	defer s.analytics.mu.Unlock()

	// Add to rolling window
	if len(s.analytics.rollingWindow) >= windowSize {
		s.analytics.rollingWindow = s.analytics.rollingWindow[1:]
	}
	s.analytics.rollingWindow = append(s.analytics.rollingWindow, rps)

	// Calculate rolling average (prediction)
	if len(s.analytics.rollingWindow) > 0 {
		sum := 0.0
		for _, v := range s.analytics.rollingWindow {
			sum += v
		}
		s.analytics.prediction = sum / float64(len(s.analytics.rollingWindow))
		predictionGauge.Set(s.analytics.prediction)
	}

	// Calculate mean and std dev for z-score
	if len(s.analytics.rollingWindow) >= windowSize {
		mean := 0.0
		for _, v := range s.analytics.rollingWindow {
			mean += v
		}
		mean /= float64(len(s.analytics.rollingWindow))
		s.analytics.mean = mean

		// Calculate standard deviation
		variance := 0.0
		for _, v := range s.analytics.rollingWindow {
			variance += (v - mean) * (v - mean)
		}
		variance /= float64(len(s.analytics.rollingWindow))
		s.analytics.stdDev = math.Sqrt(variance)

		// Z-score anomaly detection
		if s.analytics.stdDev > 0 {
			zScore := (rps - mean) / s.analytics.stdDev
			if zScore > zScoreThreshold || zScore < -zScoreThreshold {
				s.analytics.anomalyCount++
				anomalyCounter.Inc()
				log.Printf("Anomaly detected: RPS=%.2f, Z-score=%.2f, Mean=%.2f, StdDev=%.2f",
					rps, zScore, mean, s.analytics.stdDev)
			}
		}
	}

	s.analytics.totalMetrics++
	if s.analytics.totalMetrics > 0 {
		s.analytics.anomalyRate = float64(s.analytics.anomalyCount) / float64(s.analytics.totalMetrics) * 100
		anomalyRateGauge.Set(s.analytics.anomalyRate)
	}
}

func (s *Service) getAnalytics(c *gin.Context) {
	start := time.Now()
	s.analytics.mu.RLock()
	defer s.analytics.mu.RUnlock()

	response := gin.H{
		"prediction":     s.analytics.prediction,
		"window_size":    len(s.analytics.rollingWindow),
		"total_metrics":  s.analytics.totalMetrics,
		"anomaly_count":  s.analytics.anomalyCount,
		"anomaly_rate":   s.analytics.anomalyRate,
		"mean":           s.analytics.mean,
		"std_dev":        s.analytics.stdDev,
		"current_window": s.analytics.rollingWindow,
	}

	latencyHistogram.WithLabelValues("analyze").Observe(time.Since(start).Seconds())
	c.JSON(http.StatusOK, response)
}

func (s *Service) health(c *gin.Context) {
	status := http.StatusOK
	health := gin.H{
		"status": "healthy",
		"redis":  "connected",
	}

	if err := s.redis.Ping(ctx).Err(); err != nil {
		status = http.StatusServiceUnavailable
		health["status"] = "unhealthy"
		health["redis"] = "disconnected"
	}

	c.JSON(status, health)
}

func main() {
	redisAddr := "localhost:6379"
	if addr := getEnv("REDIS_ADDR", ""); addr != "" {
		redisAddr = addr
	}

	service, err := NewService(redisAddr)
	if err != nil {
		log.Fatalf("Failed to initialize service: %v", err)
	}

	// Set Gin to release mode for production
	if getEnv("GIN_MODE", "") == "release" {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.Default()

	r.GET("/health", service.health)

	r.POST("/metrics", service.ingestMetric)

	r.GET("/analyze", service.getAnalytics)

	r.GET("/metrics/prometheus", gin.WrapH(promhttp.Handler()))

	port := getEnv("PORT", "8080")
	log.Printf("Server starting on port %s", port)
	log.Fatal(http.ListenAndServe(":"+port, r))
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
