// Package templates holds the built-in format configuration templates that
// Paily Core seeds into the database on first startup.
package templates

import (
	_ "embed"
	"encoding/json"
)

// clashTemplate is a complete Clash/Mihomo YAML configuration. The Clash
// generator replaces its top-level "proxies" list and appends every pushed node
// name to each proxy-group.
//
//go:embed clash.yaml
var clashTemplate []byte

// singboxTemplate is a complete sing-box JSON configuration. The sing-box
// generator keeps its management outbounds and replaces the proxy outbounds.
//
//go:embed singbox.json
var singboxTemplate []byte

// Default returns the built-in format config for the given format name encoded
// as the {"template": "<raw template>"} object expected by the generators.
// It returns (nil, false) for formats that have no built-in template.
func Default(formatName string) ([]byte, bool) {
	switch formatName {
	case "clash":
		return wrap(clashTemplate), true
	case "singbox":
		return wrap(singboxTemplate), true
	default:
		return nil, false
	}
}

// wrap encodes a raw template as the {"template": "..."} config object.
func wrap(raw []byte) []byte {
	cfg, err := json.Marshal(struct {
		Template string `json:"template"`
	}{Template: string(raw)})
	if err != nil {
		return []byte("{}")
	}
	return cfg
}
