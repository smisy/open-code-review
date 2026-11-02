// Package argusaccepth holds throwaway code for the Argus acceptance run; it is never merged.
package argusaccepth

import (
	"encoding/json"
	"errors"
	"os"
)

// Config is a small settings file.
type Config struct {
	Name    string `json:"name"`
	Retries int    `json:"retries"`
}

// LoadConfig reads the JSON config at path. A missing file gives the zero Config.
func LoadConfig(path string) (Config, error) {
	var c Config
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return c, nil
		}
		return c, err
	}
	json.Unmarshal(data, &c)
	return c, nil
}
