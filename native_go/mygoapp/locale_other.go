//go:build !windows

package mygoapp

import "os"

func systemLocale() string { return ResolveLocale(os.Getenv("LANG")) }
