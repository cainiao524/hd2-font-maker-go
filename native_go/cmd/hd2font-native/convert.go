package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"hd2font/native/internal/fontcore"
)

func convert() error {
	args := flag.NewFlagSet("convert", flag.ContinueOnError)
	input := args.String("input", "", "input static TrueType font")
	output := args.String("output", "", "new output OTF (same units per em)")
	workers := args.Int("workers", 0, "native glyph workers")
	if err := args.Parse(os.Args[2:]); err != nil {
		return err
	}
	if *input == "" || *output == "" {
		return fmt.Errorf("input and output are required")
	}
	if _, err := os.Stat(*output); !os.IsNotExist(err) {
		return fmt.Errorf("output must be a new file: %s", *output)
	}
	data, err := os.ReadFile(*input)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	result, report, err := fontcore.ToCFF(ctx, data, *workers)
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
		fontcore.CFFReport
	}{"native-cff-stage-only-same-upm", false, false, report})
}
