package mygoapp

import (
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestV13PortSurface(t *testing.T) {
	if Version != "2.1.2" {
		t.Fatalf("version = %q", Version)
	}
	if !slices.Equal(gameLanguages, []string{"sc", "tc", "latin", "jp", "ko"}) {
		t.Fatalf("game font groups = %v", gameLanguages)
	}
	if len(Locales) != 14 || len(catalogs) != 14 {
		t.Fatalf("interface locales: list=%d catalogs=%d", len(Locales), len(catalogs))
	}
	for _, locale := range Locales {
		messages := catalogs[locale.Code]
		if messages["scope.mainGroups"] == "" {
			t.Errorf("%s lacks the main-group explanation", locale.Code)
		}
		if _, old := messages["scope.supplemental"]; old {
			t.Errorf("%s retains the superseded supplemental-scope explanation", locale.Code)
		}
	}

	a := readyApp(t, &recordingBackend{requests: make(chan BuildRequest, 1)})
	tt := ui.NewTester(a.view, 1140, 1000)
	if err := tt.Click(a.t("preset.none")); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click(a.t("group.latin")); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(a.settings.Languages, []string{"latin"}) {
		t.Fatalf("Latin preset selected %v", a.settings.Languages)
	}
	if err := tt.Click(a.t("symbols.toggle")); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(a.settings.Languages, []string{"latin"}) || !a.settings.IncludeSymbols {
		t.Fatalf("symbol option changed game groups: %v, symbols=%v", a.settings.Languages, a.settings.IncludeSymbols)
	}
	if !tt.HasText(a.t("scope.mainGroups")) {
		t.Fatal("main-group explanation is not visible")
	}
}
