//go:build windows && amd64

package main

import (
	"path/filepath"
	"testing"

	"hd2font/native/mygoapp"
	"hd2font/native/mygoapp/production"
)

func TestDefaultEntryUsesProductionAndPortableSettings(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "hd2font.exe")
	var opts mygoapp.Options
	closeBackend, err := configureBackend(executable, &opts)
	if err != nil {
		t.Fatal(err)
	}
	defer closeBackend()
	if _, ok := opts.Backend.(*production.Backend); !ok {
		t.Fatalf("default backend is %T", opts.Backend)
	}
	if opts.SettingsPath != filepath.Join(filepath.Dir(executable), "UserData", "mygo-settings.json") {
		t.Fatal("settings not beside executable")
	}
	opts = mygoapp.Options{Demo: true}
	closeDemo, err := configureBackend(executable, &opts)
	if err != nil {
		t.Fatal(err)
	}
	defer closeDemo()
	if _, ok := opts.Backend.(demoBackend); !ok {
		t.Fatal("explicit demo not isolated")
	}
}
