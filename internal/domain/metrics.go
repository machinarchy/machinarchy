package domain

import (
	"context"
	"time"
)

type SystemStats struct {
	CPUPercent float64 `json:"cpuPercent"`
	MemUsed    uint64  `json:"memUsed"`
	MemPercent float64 `json:"memPercent"`
}

// DataPoint is our core domain representation of a system metrics snapshot
type DataPoint struct {
	Timestamp  string  `json:"timestamp"`
	CPUPercent float64 `json:"cpuPercent"`
	MemPercent float64 `json:"memPercent"`
	CreatedAt  string  `json:"created_at"`
}

type HostRuntimeDiagnostics struct {
	HostID       int       `json:"hostId"`
	Timestamp    time.Time `json:"timestamp"`
	Uptime       string    `json:"uptime"`
	CPUUsagePct  float64   `json:"cpuUsagePct"`
	MemoryTotal  int       `json:"memoryTotalMB"`
	MemoryUsed   int       `json:"memoryUsedMB"`
	MemoryFree   int       `json:"memoryFreeMB"`
	DiskTotalGB  float64   `json:"diskTotalGB"`
	DiskUsedGB   float64   `json:"diskUsedGB"`
	DiskUsagePct float64   `json:"diskUsagePct"`
}


type TelemetryReader interface {
	ReadCurrentStats() SystemStats
}

// MetricsRepository defines how your app queries and stores metrics in SQLite
type MetricsStore interface {
	SaveMetricPoint(ctx context.Context, cpuPct, memPct float64) error
	GetStoredHistory(ctx context.Context, limit int) ([]DataPoint, error)
}

// MetricsService handles your time-range conversion logic
type MetricsService interface {
	GetHistory(ctx context.Context, timeRange string) ([]DataPoint, error)
	GetLiveDiagnostics(ctx context.Context, hostID int) (*HostRuntimeDiagnostics, error)
}
