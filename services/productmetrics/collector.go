package productmetrics

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
)

type metricKey struct {
	Day        string
	TenantCode string
	ClientID   uuid.UUID
}

type counters struct {
	Requests     int64
	Mutations    int64
	Errors       int64
	DurationMS   int64
	RequestBytes int64
}

type activeUserKey struct {
	metricKey
	UserID uuid.UUID
}

type Collector struct {
	db      *gorm.DB
	mu      sync.Mutex
	pending map[metricKey]counters
	active  map[activeUserKey]struct{}
}

var defaultCollector *Collector

// RequestObservation is framework-neutral input collected by transport adapters.
type RequestObservation struct {
	TenantCode, Method     string
	OrganizationID, UserID uuid.UUID
	Status                 int
	Duration               time.Duration
	RequestBytes           int64
}

func NewCollector(db *gorm.DB) *Collector {
	return &Collector{
		db:      db,
		pending: make(map[metricKey]counters),
		active:  make(map[activeUserKey]struct{}),
	}
}

func Configure(collector *Collector) { defaultCollector = collector }
func DefaultCollector() *Collector   { return defaultCollector }

func (collector *Collector) Record(observation RequestObservation) {
	if collector == nil || collector.db == nil {
		return
	}
	tenant := strings.ToLower(strings.TrimSpace(observation.TenantCode))
	if tenant == "" {
		return
	}
	key := metricKey{
		Day:        time.Now().UTC().Format("2006-01-02"),
		TenantCode: tenant,
		ClientID:   observation.OrganizationID,
	}
	value := counters{
		Requests:     1,
		DurationMS:   observation.Duration.Milliseconds(),
		RequestBytes: max(observation.RequestBytes, 0),
	}
	if isMutation(observation.Method) && observation.Status >= http.StatusOK && observation.Status < http.StatusBadRequest {
		value.Mutations = 1
	}
	if observation.Status >= http.StatusInternalServerError {
		value.Errors = 1
	}

	collector.mu.Lock()
	current := collector.pending[key]
	current.Requests += value.Requests
	current.Mutations += value.Mutations
	current.Errors += value.Errors
	current.DurationMS += value.DurationMS
	current.RequestBytes += value.RequestBytes
	collector.pending[key] = current
	if observation.UserID != uuid.Nil {
		collector.active[activeUserKey{metricKey: key, UserID: observation.UserID}] = struct{}{}
	}
	collector.mu.Unlock()
}

func (collector *Collector) Start(ctx context.Context) {
	if collector == nil {
		return
	}
	ticker := time.NewTicker(30 * time.Second)
	collector.prune(context.Background())
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				collector.Flush(context.Background())
			case <-ctx.Done():
				collector.Flush(context.Background())
				return
			}
		}
	}()
}

func (collector *Collector) prune(ctx context.Context) {
	cutoff := time.Now().UTC().AddDate(-2, -1, 0)
	for _, table := range []string{"product_active_user_daily", "product_metric_daily"} {
		if err := collector.db.WithContext(ctx).Exec("DELETE FROM "+table+" WHERE day < ?", cutoff).Error; err != nil {
			slog.Warn("product metrics retention failed", "table", table, "error", err)
		}
	}
}

func (collector *Collector) Flush(ctx context.Context) {
	if collector == nil || collector.db == nil {
		return
	}
	collector.mu.Lock()
	pending := collector.pending
	active := collector.active
	collector.pending = make(map[metricKey]counters)
	collector.active = make(map[activeUserKey]struct{})
	collector.mu.Unlock()

	for key, value := range pending {
		err := collector.db.WithContext(ctx).Exec(`
			INSERT INTO product_metric_daily
				(day, tenant_code, client_id, requests, mutations, errors, duration_ms, request_bytes, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, NOW(), NOW())
			ON CONFLICT (day, tenant_code, client_id)
			DO UPDATE SET
				requests = product_metric_daily.requests + EXCLUDED.requests,
				mutations = product_metric_daily.mutations + EXCLUDED.mutations,
				errors = product_metric_daily.errors + EXCLUDED.errors,
				duration_ms = product_metric_daily.duration_ms + EXCLUDED.duration_ms,
				request_bytes = product_metric_daily.request_bytes + EXCLUDED.request_bytes,
				updated_at = NOW()
		`, key.Day, key.TenantCode, key.ClientID, value.Requests, value.Mutations, value.Errors, value.DurationMS, value.RequestBytes).Error
		if err != nil {
			slog.Error("product metrics flush failed", "tenant", key.TenantCode, "error", err)
			collector.restore(key, value)
		}
	}
	for key := range active {
		err := collector.db.WithContext(ctx).Exec(`
			INSERT INTO product_active_user_daily (day, tenant_code, client_id, user_id, created_at)
			VALUES (?, ?, ?, ?, NOW())
			ON CONFLICT DO NOTHING
		`, key.Day, key.TenantCode, key.ClientID, key.UserID).Error
		if err != nil {
			slog.Error("active user metric flush failed", "tenant", key.TenantCode, "error", err)
			collector.mu.Lock()
			collector.active[key] = struct{}{}
			collector.mu.Unlock()
		}
	}
}

func (collector *Collector) restore(key metricKey, value counters) {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	current := collector.pending[key]
	current.Requests += value.Requests
	current.Mutations += value.Mutations
	current.Errors += value.Errors
	current.DurationMS += value.DurationMS
	current.RequestBytes += value.RequestBytes
	collector.pending[key] = current
}

func isMutation(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}
