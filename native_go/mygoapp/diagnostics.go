package mygoapp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

func appendErrorDiagnostic(settingsPath, key, detail string) error {
	dir := filepath.Dir(settingsPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "frontend.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	err = json.NewEncoder(f).Encode(struct {
		Timestamp time.Time `json:"timestamp"`
		ErrorKey  string    `json:"error_key"`
		Detail    string    `json:"detail"`
	}{Timestamp: time.Now().UTC(), ErrorKey: key, Detail: detail})
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}
