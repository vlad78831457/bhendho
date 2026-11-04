// Package contracts отдаёт языконезависимые контракты ядра (JSON Schema)
// как встроенную файловую систему — единственный источник правды для Go-кода.
package contracts

import "embed"

// FS содержит схемы манифеста, конверта бланка, realtime-событий, примеры и договоры интерфейсов (OpenAPI).
//
//go:embed manifests/*.json events/*.json manifests/examples/* interfaces/*.yaml
var FS embed.FS
