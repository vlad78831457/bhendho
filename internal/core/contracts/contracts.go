// Package contracts компилирует JSON-схемы из contracts/ и проверяет по ним
// манифесты модулей, конверты бланков и realtime-события.
package contracts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"offgrid/core/contracts"
)

// Идентификаторы схем совпадают с их $id.
const (
	ManifestSchemaID = "https://r-aaa-core-a.io/schemas/module_manifest.json"
	BlankSchemaID    = "https://r-aaa-core-a.io/schemas/blank_envelope.json"
	EventSchemaID    = "https://r-aaa-core-a.io/schemas/realtime_event.json"
)

var schemaFiles = map[string]string{
	ManifestSchemaID: "manifests/module_schema.json",
	BlankSchemaID:    "manifests/blank_envelope_schema.json",
	EventSchemaID:    "events/realtime_events.schema.json",
}

// Validator держит скомпилированные схемы; безопасен для конкурентного использования.
type Validator struct {
	manifest *jsonschema.Schema
	blank    *jsonschema.Schema
	event    *jsonschema.Schema
}

// Load компилирует все схемы из встроенной FS контрактов.
func Load() (*Validator, error) {
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	for id, path := range schemaFiles {
		raw, err := fs.ReadFile(contracts.FS, path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		if err := c.AddResource(id, doc); err != nil {
			return nil, fmt.Errorf("add %s: %w", path, err)
		}
	}
	v := &Validator{}
	for id, dst := range map[string]**jsonschema.Schema{
		ManifestSchemaID: &v.manifest,
		BlankSchemaID:    &v.blank,
		EventSchemaID:    &v.event,
	} {
		s, err := c.Compile(id)
		if err != nil {
			return nil, fmt.Errorf("compile %s: %w", id, err)
		}
		*dst = s
	}
	return v, nil
}

// ValidateBlank проверяет конверт бланка (JSON).
func (v *Validator) ValidateBlank(raw []byte) error {
	return validateJSON(v.blank, raw)
}

// ValidateEvent проверяет realtime-событие (JSON).
func (v *Validator) ValidateEvent(raw []byte) error {
	return validateJSON(v.event, raw)
}

// ValidateManifestYAML проверяет module.yaml по схеме и правилам, которые
// JSON Schema не выражает (уникальность единиц учёта, ADR-47).
func (v *Validator) ValidateManifestYAML(raw []byte) error {
	var doc any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("manifest yaml: %w", err)
	}
	asJSON, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("manifest to json: %w", err)
	}
	if err := validateJSON(v.manifest, asJSON); err != nil {
		return err
	}
	var m struct {
		WorkerAccounting *struct {
			BaseUnit   struct{ Name string }   `json:"base_unit"`
			ExtraUnits []struct{ Name string } `json:"extra_units"`
		} `json:"worker_accounting"`
	}
	if err := json.Unmarshal(asJSON, &m); err != nil {
		return fmt.Errorf("manifest decode: %w", err)
	}
	if wa := m.WorkerAccounting; wa != nil {
		seen := map[string]bool{wa.BaseUnit.Name: true}
		for _, u := range wa.ExtraUnits {
			if seen[u.Name] {
				return fmt.Errorf("worker_accounting: unit %q declared twice", u.Name)
			}
			seen[u.Name] = true
		}
	}
	return nil
}

func validateJSON(s *jsonschema.Schema, raw []byte) error {
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("invalid json: %w", err)
	}
	return s.Validate(inst)
}
