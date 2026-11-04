package contracts

import (
	"fmt"
	"io/fs"
	"strings"

	"gopkg.in/yaml.v3"

	"offgrid/core/contracts"
)

// GatewayFile — договор шлюза задач; его info.version ядро говорит прорабу в каждом ответе (ADR-82).
const GatewayFile = "interfaces/worker_gateway.openapi.yaml"

// InterfaceVersion — версия договора интерфейса (info.version) из встроенной FS: одна правда — сам файл договора.
func InterfaceVersion(path string) (string, error) {
	raw, err := fs.ReadFile(contracts.FS, path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	var doc struct {
		Info struct {
			Version string `yaml:"version"`
		} `yaml:"info"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return "", fmt.Errorf("parse %s: %w", path, err)
	}
	if doc.Info.Version == "" {
		return "", fmt.Errorf("%s: no info.version", path)
	}
	return doc.Info.Version, nil
}

// Major — старшая часть версии: «0.3.0» → «0», «1.2» → «1». Разные старшие — несовместимы.
func Major(version string) string {
	major, _, _ := strings.Cut(strings.TrimSpace(version), ".")
	return major
}
