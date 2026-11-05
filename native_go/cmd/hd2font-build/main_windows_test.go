package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestHelpAndInvalidArguments(t *testing.T) {
	var output, progress bytes.Buffer
	if err := run(context.Background(), []string{"--help"}, &output, &progress); err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"-snapshot", "-font", "-output", "-weights", "-single-font-weight", "-outlines", "-languages", "-bin"} {
		if !strings.Contains(progress.String(), flag) {
			t.Fatal("missing CLI option", flag)
		}
	}
	if err := run(context.Background(), []string{"--single-font-weight", "300"}, &output, &progress); err == nil || !strings.Contains(err.Error(), "400, 500, or 700") {
		t.Fatalf("invalid source assignment was not rejected: %v", err)
	}
	for _, args := range [][]string{{}, {"--snapshot", "unused", "--font", "unused", "--output", "unused.zip", "--axes", "wght=NaN"}, {"--snapshot", "unused", "--font", "unused", "--output", "unused.zip", "--languages", "arabic"}, {"--snapshot", "unused", "--font", "unused", "--output", "unused.zip", "--axes", "wght=400,wght=700"}} {
		if err := run(context.Background(), args, &output, &progress); err == nil {
			t.Fatal("invalid arguments accepted", args)
		}
	}
}
