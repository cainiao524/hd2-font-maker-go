package mygoapp

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const templateNameError = "template names: invalid name language reference"

func readDiagnostics(t *testing.T, settingsPath string) []map[string]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(filepath.Dir(settingsPath), "frontend.log"))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Fatal("diagnostics must contain newline-terminated entries")
	}
	var entries []map[string]string
	for _, line := range bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n")) {
		var entry map[string]string
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatalf("invalid diagnostic entry: %v", err)
		}
		if len(entry) != 3 {
			t.Fatalf("unexpected diagnostic fields: %v", entry)
		}
		if _, err := time.Parse(time.RFC3339Nano, entry["timestamp"]); err != nil {
			t.Fatalf("invalid timestamp: %v", err)
		}
		entries = append(entries, entry)
	}
	return entries
}

func TestDiagnosticsWriteAndAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "UserData", "mygo-settings.json")
	a := &application{settingsPath: path, settings: DefaultSettings()}
	a.settings.PreviewText = "private-preview-text"
	a.settings.Font.Path = "private-font-path.ttf"
	a.setError("error.failed", errors.New(templateNameError))
	logPath := filepath.Join(filepath.Dir(path), "frontend.log")
	first, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	// A fresh application must append to diagnostics from the previous instance.
	b := &application{settingsPath: path}
	detail := "font read failed\nsecond line with \"quotes\""
	b.setError("error.font_read", errors.New(detail))
	b.setError("error.no_font", nil)
	b.setError("", nil)
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, first) || bytes.Contains(data, []byte("private-")) {
		t.Fatal("diagnostics replaced an earlier entry or included personal settings")
	}
	entries := readDiagnostics(t, path)
	if len(entries) != 3 || entries[0]["error_key"] != "error.failed" || entries[0]["detail"] != templateNameError ||
		entries[1]["error_key"] != "error.font_read" || entries[1]["detail"] != detail ||
		entries[2]["error_key"] != "error.no_font" || entries[2]["detail"] != "" {
		t.Fatalf("wrong diagnostics: %v", entries)
	}
	if a.errorKey != "error.failed" || a.errorDetail != templateNameError || b.errorKey != "" || b.errorDetail != "" {
		t.Fatal("logging changed UI error state")
	}
}

func TestDiagnosticsFailureRetainsOriginalError(t *testing.T) {
	for _, failure := range []string{"parent-is-file", "log-is-directory"} {
		t.Run(failure, func(t *testing.T) {
			dir := t.TempDir()
			if failure == "parent-is-file" {
				dir = filepath.Join(dir, "UserData")
				if err := os.WriteFile(dir, []byte("blocked"), 0600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Mkdir(filepath.Join(dir, "frontend.log"), 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "mygo-settings.json")
			if err := appendErrorDiagnostic(path, "error.failed", templateNameError); err == nil {
				t.Fatal("expected a diagnostic write failure")
			}
			a := &application{settingsPath: path}
			a.setError("error.failed", errors.New(templateNameError))
			if a.errorKey != "error.failed" || a.errorDetail != templateNameError {
				t.Fatalf("logging failure replaced original error: %s %s", a.errorKey, a.errorDetail)
			}
		})
	}
}

func TestDiagnosticsGenerationError(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		name := "writable-log"
		if blocked {
			name = "blocked-log"
		}
		t.Run(name, func(t *testing.T) {
			b := &recordingBackend{requests: make(chan BuildRequest, 1), failure: errors.New(templateNameError)}
			a := readyApp(t, b)
			if blocked {
				if err := os.Mkdir(filepath.Join(filepath.Dir(a.settingsPath), "frontend.log"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			a.generate()
			awaitState(t, a, func() bool { return !a.busy })
			if a.errorKey != "error.failed" || a.errorDetail != templateNameError || a.statusKey != "error.failed" {
				t.Fatalf("generation error lost: %s %s %s", a.errorKey, a.errorDetail, a.statusKey)
			}
			if !blocked {
				entries := readDiagnostics(t, a.settingsPath)
				if len(entries) != 1 || entries[0]["error_key"] != "error.failed" || entries[0]["detail"] != templateNameError {
					t.Fatalf("generation error not persisted: %v", entries)
				}
			}
		})
	}
}

func TestDiagnosticsSettingsErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mygo-settings.json")
	// A directory at the settings path makes both reading and saving fail.
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	a, err := newApplication(Options{SettingsPath: path, Locale: "en"})
	if err != nil {
		t.Fatal(err)
	}
	loadDetail := a.errorDetail
	if a.errorKey != "error.failed" || loadDetail == "" {
		t.Fatal("settings load error not displayed")
	}
	a.save()
	if a.errorKey != "error.failed" || a.errorDetail == "" {
		t.Fatal("settings save error not displayed")
	}
	entries := readDiagnostics(t, path)
	if len(entries) != 2 || entries[0]["error_key"] != "error.failed" || entries[0]["detail"] != loadDetail ||
		entries[1]["error_key"] != "error.failed" || entries[1]["detail"] != a.errorDetail {
		t.Fatalf("settings errors not persisted: %v", entries)
	}
}
