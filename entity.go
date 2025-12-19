package main

// Metric структура для представления метрики
type MetricDto struct {
	Name      string  `json:"name" binding:"required"`      // Название метрики
	Value     float64 `json:"value" binding:"required"`     // Значение метрики
	Timestamp int64   `json:"timestamp" binding:"required"` // Временной штамп
}

type Analytics struct {
	totalCount   int64
	anomalyCount int64
	prediction   float64
}
