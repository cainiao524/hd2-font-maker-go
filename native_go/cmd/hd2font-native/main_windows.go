//go:build windows && amd64

package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"

	"hd2font/native/internal/fontcore"
	"hd2font/native/internal/geometry"
)

func run() error {
	if len(os.Args) > 1 && os.Args[1] == "convert" {
		return convert()
	}
	if len(os.Args) < 2 || os.Args[1] != "weight" {
		return fmt.Errorf("development engine: hd2font-native weight -input font.ttf -output weight.ttf [-weight 700] [-outlines=false]")
	}
	args := flag.NewFlagSet("weight", flag.ContinueOnError)
	input := args.String("input", "", "input static TrueType font")
	output := args.String("output", "", "new output font (must not exist)")
	weight := args.Int("weight", 700, "target emphasis weight")
	workers := args.Int("workers", 0, "native glyph workers, 0 selects automatically")
	pixel := args.Bool("pixel-grid", false, "snap emphasis radius to 4 font units")
	outlines := args.Bool("outlines", false, "process degenerate contours")
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	native := args.String("native", filepath.Dir(exe), "native library directory")
	if err = args.Parse(os.Args[2:]); err != nil {
		return err
	}
	if *input == "" || *output == "" {
		return fmt.Errorf("input and output are required")
	}
	if _, err = os.Stat(*output); !os.IsNotExist(err) {
		return fmt.Errorf("output must be a new file: %s", *output)
	}
	data, err := os.ReadFile(*input)
	if err != nil {
		return err
	}
	engine, err := geometry.Open(*native)
	if err != nil {
		return err
	}
	defer engine.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	result, report, err := fontcore.Weight(ctx, engine, data, fontcore.WeightOptions{Weight: *weight, Workers: *workers, PixelGrid: *pixel, ProcessOutlines: *outlines})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(result)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		Mode          string `json:"mode"`
		PythonRuntime bool   `json:"python_runtime"`
		CacheUsed     bool   `json:"cache_used"`
		InputSHA256   string `json:"input_sha256"`
		OutputSHA256  string `json:"output_sha256"`
		fontcore.WeightReport
	}{"native-weight-stage-only", false, false, fmt.Sprintf("%x", sha256.Sum256(data)), fmt.Sprintf("%x", sha256.Sum256(result)), report})
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
