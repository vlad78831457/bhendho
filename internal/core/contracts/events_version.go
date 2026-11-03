package contracts

import (
	"encoding/json"
	"fmt"
	"io/fs"

	"offgrid/core/contracts"
)

// EventsFile — реестр realtime-событий; его версия — const поля v (ADR-83).
const EventsFile = "events/realtime_events.schema.json"

// EventsVersion — версия реестра событий из встроенной схемы: properties.v.const.
func EventsVersion() (string, error) {
	raw, err := fs.ReadFile(contracts.FS, EventsFile)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", EventsFile, err)
	}
	var doc struct {
		Properties struct {
			V struct {
				Const string `json:"const"`
			} `json:"v"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", fmt.Errorf("parse %s: %w", EventsFile, err)
	}
	if doc.Properties.V.Const == "" {
		return "", fmt.Errorf("%s: no properties.v.const", EventsFile)
	}
	return doc.Properties.V.Const, nil
}
