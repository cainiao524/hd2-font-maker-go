package mygoapp

import (
	"embed"
	"encoding/json"
	"strings"
)

// Catalogs provide translations for the native UI in fourteen languages.
//
//go:embed locales/*.json
var catalogFiles embed.FS

type Locale struct{ Code, Name string }

var Locales = []Locale{
	{"en", "English"}, {"fr", "Français"}, {"it", "Italiano"}, {"de", "Deutsch"},
	{"es-ES", "Español (España)"}, {"ja", "日本語"}, {"ko", "한국어"},
	{"pt-BR", "Português (Brasil)"}, {"es-419", "Español (Latinoamérica)"},
	{"pl", "Polski"}, {"pt-PT", "Português (Portugal)"}, {"ru", "Русский"},
	{"zh-CN", "简体中文"}, {"zh-TW", "繁體中文"},
}

var catalogs = func() map[string]map[string]string {
	all := make(map[string]map[string]string)
	for _, locale := range Locales {
		data, err := catalogFiles.ReadFile("locales/" + locale.Code + ".json")
		if err != nil {
			panic(err)
		}
		var messages map[string]string
		if err := json.Unmarshal(data, &messages); err != nil {
			panic(err)
		}
		all[locale.Code] = messages
	}
	return all
}()

func ResolveLocale(code string) string {
	code = strings.ToLower(strings.ReplaceAll(code, "_", "-"))
	switch {
	case strings.HasPrefix(code, "zh"):
		if strings.Contains(code, "tw") || strings.Contains(code, "hk") || strings.Contains(code, "hant") {
			return "zh-TW"
		}
		return "zh-CN"
	case strings.HasPrefix(code, "pt"):
		if strings.Contains(code, "br") {
			return "pt-BR"
		}
		return "pt-PT"
	case strings.HasPrefix(code, "es"):
		if code == "es" || code == "es-es" {
			return "es-ES"
		}
		return "es-419"
	}
	base := strings.Split(code, "-")[0]
	if _, ok := catalogs[base]; ok {
		return base
	}
	return "en"
}

func Translate(locale, key string, pairs ...string) string {
	value := catalogs[ResolveLocale(locale)][key]
	if value == "" {
		value = catalogs["en"][key]
	}
	if value == "" {
		value = key
	}
	for i := 0; i+1 < len(pairs); i += 2 {
		value = strings.ReplaceAll(value, "{"+pairs[i]+"}", pairs[i+1])
	}
	return value
}

func uiFont(locale string) string {
	switch locale {
	case "zh-CN":
		return "Microsoft YaHei UI"
	case "zh-TW":
		return "Microsoft JhengHei UI"
	case "ja":
		return "Yu Gothic UI"
	case "ko":
		return "Malgun Gothic"
	}
	return "Segoe UI"
}
