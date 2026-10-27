//go:build windows && amd64

package production

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	"hd2font/native/internal/inventory"
	"hd2font/native/mygoapp"
)

type scanCancelled struct{}

// inventory.Scan currently has a progress callback but no context argument.
// This private sentinel unwinds only our cancelled scan at its next callback;
// other panics remain panics. No detached scan continues after cancellation.
func scanInventory(ctx context.Context, path string, progress func(string)) (inv *inventory.Inventory, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	defer func() {
		if value := recover(); value != nil {
			if _, ok := value.(scanCancelled); ok {
				inv = nil
				err = ctx.Err()
			} else {
				panic(value)
			}
		}
	}()
	inv, err = inventory.Scan(path, func(message string) {
		if ctx.Err() != nil {
			panic(scanCancelled{})
		}
		progress(message)
	})
	if err == nil {
		err = ctx.Err()
	}
	return inv, err
}

var counts = regexp.MustCompile(`(\d+)\s*/\s*(\d+)`)

func progressMessage(message, fallback string) mygoapp.Progress {
	lower := strings.ToLower(message)
	stage := fallback
	// These pipeline messages do not contain the generic phase keywords, or
	// contain "resource" after atlas generation, when preparation is over.
	for _, entry := range []struct{ prefix, stage string }{
		{"preparing selected font", "source"},
		{"preparing family faces", "source"},
		{"found ", "scope"},
		{"writing and reading back font resources", "package"},
	} {
		if strings.HasPrefix(lower, entry.prefix) {
			stage = entry.stage
			goto found
		}
	}
	for _, entry := range []struct {
		stage string
		terms []string
	}{
		{"package", []string{"zip", "packag", "打包", "压缩写出"}},
		{"atlas", []string{"atlas", "texture", "贴图"}},
		{"register", []string{"register", "注册", "登记"}},
		{"merge", []string{"merg", "fallback", "合并"}},
		{"weights", []string{"weight", "字重"}},
		{"outlines", []string{"outline", "轮廓"}},
		{"convert", []string{"conform", "convert", "适配", "转换"}},
		{"scope", []string{"scope", "language", "资源组"}},
		{"source", []string{"source", "custom font", "自选字体"}},
		{"scan", []string{"scan", "index", "读取当前游戏", "reading game"}},
	} {
		for _, term := range entry.terms {
			if strings.Contains(lower, term) {
				stage = entry.stage
				goto found
			}
		}
	}
found:
	p := mygoapp.Progress{Stage: stage, Fraction: -1, Detail: message}
	if match := counts.FindStringSubmatch(message); len(match) == 3 {
		p.Completed, _ = strconv.ParseInt(match[1], 10, 64)
		p.Total, _ = strconv.ParseInt(match[2], 10, 64)
	}
	p.Fraction = phaseFraction(p)
	return p
}

// Phase budgets express completed work, not elapsed time or an ETA. Batch
// counts only fill their phase; ZIP writes and readback must not display 100%.
func phaseFraction(p mygoapp.Progress) float64 {
	var start, end float64
	switch p.Stage {
	case "scan":
		start, end = 0, .05
	case "scope":
		start, end = .05, .05
	case "source":
		start, end = .05, .15
	case "weights":
		start, end = .15, .35
	case "merge":
		start, end = .35, .5
	case "outlines":
		start, end = .5, .55
	case "convert":
		start, end = .5, .65
	case "register":
		start, end = .65, .7
	case "atlas":
		start, end = .7, .9
	case "package":
		start, end = .9, .99
	default:
		return -1
	}
	if p.Total <= 0 {
		return start
	}
	ratio := max(0, min(1, float64(p.Completed)/float64(p.Total)))
	return start + (end-start)*ratio
}

// A job can emit uncounted messages between batches. Retain the last known
// progress in those gaps, and start fresh for every Generate call.
func overallProgress(report func(mygoapp.Progress)) func(mygoapp.Progress) {
	last := mygoapp.Progress{Stage: "scan"}
	return func(p mygoapp.Progress) {
		p.Fraction = max(last.Fraction, min(1, p.Fraction))
		if p.Stage == "working" || p.Stage == "" {
			p.Stage = last.Stage
		}
		last = p
		report(p)
	}
}
