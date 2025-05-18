// path: internal/constants/cache_ttl.go
package constants

import "time"

const (
	ShortTimeTTL  = 1 * time.Hour   // Para datos que cambian constantemente
	MediumTimeTTL = 8 * time.Hour   // Uso general
	LargeTimeTTL  = 24 * time.Hour  // Datos semiestáticos
	LongTimeTTL   = 48 * time.Hour  // Poco cambio
	XLongTimeTTL  = 96 * time.Hour  // Muy estáticos
	XXLongTimeTTL = 168 * time.Hour // 7 días
)
