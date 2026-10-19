package mygoapp

import (
	"embed"
	"github.com/egoist/mygo/ui"
)

// Lucide 0.468.0, ISC; license accompanies the pinned SVG assets.
//
//go:embed icons/*.svg
var iconFiles embed.FS

var icons = func() map[string]*ui.SVG {
	out := map[string]*ui.SVG{}
	for _, name := range []string{"folder-open", "file-type", "refresh-cw", "archive", "folder", "x"} {
		data, err := iconFiles.ReadFile("icons/" + name + ".svg")
		if err != nil {
			panic(err)
		}
		out[name] = ui.MustParseSVG(data)
	}
	return out
}()
