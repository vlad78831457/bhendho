// Package contracts отдаёт языконезависимые контракты ядра (JSON Schema)
// как встроенную файловую систему — единственный источник правды для Go-кода.
package contracts

import "embed"

// FS содержит схемы манифеста, конверта бланка, realtime-событий и примеры.
//
//go:embed manifests/*.json events/*.json manifests/examples/*
var FS embed.FS
