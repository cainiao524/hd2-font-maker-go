package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"

	"hd2font/native/internal/fontcore"
	"hd2font/native/internal/inventory"
	"hd2font/native/internal/pipeline"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, output, progress io.Writer) error {
	options := pipeline.DefaultOptions()
	flags := flag.NewFlagSet("hd2font-build", flag.ContinueOnError)
	flags.SetOutput(progress)
	var snapshot, languages, axisValues string
	executable, _ := os.Executable()
	flags.StringVar(&snapshot, "snapshot", "", "portable inventory snapshot directory")
	flags.StringVar(&options.FontPath, "font", "", "custom TTF, OTF or collection")
	flags.StringVar(&options.OutputZIP, "output", "", "output ZIP path")
	flags.IntVar(&options.FaceIndex, "face", 0, "collection face index")
	flags.StringVar(&axisValues, "axes", "", "variation coordinates, e.g. wght=400,opsz=16")
	flags.StringVar(&languages, "languages", "sc", "comma-separated UI languages: sc,tc,latin,jp,ko")
	flags.BoolVar(&options.MatchWeights, "weights", true, "match original game font weights")
	flags.BoolVar(&options.ProcessOutlines, "outlines", false, "process font outlines")
	flags.BoolVar(&options.IncludeSymbols, "symbols", false, "include covered symbol font groups")
	flags.StringVar(&options.BinDir, "bin", filepath.Dir(executable), "directory containing native font libraries")
	flags.StringVar(&options.WorkDir, "work", "", "temporary workspace parent directory")
	flags.StringVar(&options.Name, "name", options.Name, "mod-manager package name")
	flags.IntVar(&options.Workers, "workers", options.Workers, "maximum independent build jobs (1 through 8)")
	flags.IntVar(&options.ZIPLevel, "zip-level", options.ZIPLevel, "ZIP compression level (0 through 9)")
	flags.StringVar(&options.AtlasExecutable, "atlas", "", "native msdf-atlas-gen executable override")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	if snapshot == "" || options.FontPath == "" || options.OutputZIP == "" {
		return fmt.Errorf("--snapshot, --font and --output are required")
	}
	options.Languages = nil
	for _, language := range strings.Split(languages, ",") {
		options.Languages = append(options.Languages, strings.TrimSpace(language))
	}
	var err error
	options.Languages, err = inventory.NormalizeLanguages(options.Languages)
	if err != nil {
		return err
	}
	options.Axes = make(map[string]float32)
	if axisValues != "" {
		for _, part := range strings.Split(axisValues, ",") {
			name, value, ok := strings.Cut(part, "=")
			name = strings.TrimSpace(name)
			if !ok || len(name) != 4 {
				return fmt.Errorf("invalid variation coordinate %q", part)
			}
			parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 32)
			if err != nil {
				return fmt.Errorf("axis %s: %w", name, err)
			}
			if math.IsNaN(parsed) || math.IsInf(parsed, 0) {
				return fmt.Errorf("axis %s must be finite", name)
			}
			if _, exists := options.Axes[name]; exists {
				return fmt.Errorf("duplicate axis %q", name)
			}
			options.Axes[name] = float32(parsed)
		}
	}
	fmt.Fprintln(progress, "Loading offline font inventory")
	inv, err := inventory.Load(snapshot)
	if err != nil {
		return err
	}
	processor, err := fontcore.NewProcessor(options.BinDir, 0)
	if err != nil {
		return err
	}
	defer processor.Close()
	report, err := pipeline.Build(ctx, inv, processor, options, func(message string) { fmt.Fprintln(progress, message) })
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}
