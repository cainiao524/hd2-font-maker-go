//go:build windows && amd64

package main

import (
	"archive/zip"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"hd2font/native/mygoapp"
	"hd2font/native/mygoapp/production"
)

func main() {
	var opts mygoapp.Options
	flag.StringVar(&opts.SettingsPath, "settings", "", "portable settings file")
	flag.StringVar(&opts.Locale, "locale", "", "interface language")
	flag.StringVar(&opts.InitialFont, "font", "", "initial font file")
	flag.IntVar(&opts.Width, "width", 1140, "initial window width in DIPs")
	flag.IntVar(&opts.Height, "height", 860, "initial window height in DIPs")
	flag.BoolVar(&opts.Demo, "demo", false, "explicit UI demo; generated ZIP is not a game mod")
	flag.Parse()
	if err := run(opts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func configureBackend(executable string, opts *mygoapp.Options) (func(), error) {
	directory := filepath.Dir(executable)
	if opts.SettingsPath == "" {
		opts.SettingsPath = filepath.Join(directory, "UserData", "mygo-settings.json")
	}
	if opts.Demo {
		opts.Backend = demoBackend{}
		return func() {}, nil
	}
	binDir := directory
	if _, err := os.Stat(filepath.Join(binDir, "hd2harfbuzz.dll")); err != nil {
		if _, nestedErr := os.Stat(filepath.Join(directory, "bin", "hd2harfbuzz.dll")); nestedErr == nil {
			binDir = filepath.Join(directory, "bin")
		}
	}
	backend, err := production.New(binDir, filepath.Dir(opts.SettingsPath))
	if err != nil {
		return nil, err
	}
	opts.Backend = backend
	return func() { _ = backend.Close() }, nil
}

func run(opts mygoapp.Options) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	closeBackend, err := configureBackend(executable, &opts)
	if err != nil {
		return err
	}
	defer closeBackend()
	return mygoapp.Run(opts)
}

// This backend is reachable only through the explicit --demo flag.
type demoBackend struct{ mygoapp.FontPreviewBackend }

func (demoBackend) Generate(ctx context.Context, req mygoapp.BuildRequest, progress func(mygoapp.Progress)) (mygoapp.BuildResult, error) {
	stages := []string{"source", "scan", "scope", "weights", "merge", "outlines", "convert", "register", "atlas", "package"}
	for i, stage := range stages {
		select {
		case <-ctx.Done():
			return mygoapp.BuildResult{}, ctx.Err()
		case <-time.After(450 * time.Millisecond):
		}
		progress(mygoapp.Progress{Stage: stage, Completed: int64(i + 1), Total: int64(len(stages)), Fraction: float64(i+1) / float64(len(stages))})
	}
	if err := os.MkdirAll(filepath.Dir(req.OutputZIP), 0755); err != nil {
		return mygoapp.BuildResult{}, err
	}
	f, err := os.CreateTemp(filepath.Dir(req.OutputZIP), "demo-*.zip")
	if err != nil {
		return mygoapp.BuildResult{}, err
	}
	defer os.Remove(f.Name())
	z := zip.NewWriter(f)
	w, err := z.Create("DEMO-ONLY.txt")
	if err == nil {
		_, err = fmt.Fprintln(w, "UI DEMONSTRATION ONLY. This is not an HD2 font mod and contains no game data.")
	}
	zipErr := z.Close()
	closeErr := f.Close()
	if err != nil {
		return mygoapp.BuildResult{}, err
	}
	if zipErr != nil {
		return mygoapp.BuildResult{}, zipErr
	}
	if closeErr != nil {
		return mygoapp.BuildResult{}, closeErr
	}
	if err := ctx.Err(); err != nil {
		return mygoapp.BuildResult{}, err
	}
	if err := os.Rename(f.Name(), req.OutputZIP); err != nil {
		return mygoapp.BuildResult{}, err
	}
	stat, err := os.Stat(req.OutputZIP)
	if err != nil {
		return mygoapp.BuildResult{}, err
	}
	return mygoapp.BuildResult{OutputZIP: req.OutputZIP, Bytes: stat.Size()}, nil
}
