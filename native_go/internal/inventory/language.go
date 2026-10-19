package inventory

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

type GroupInfo struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Sample      string `json:"sample"`
}

// GroupDefinitions returns the Python GROUPS order and user-facing metadata.
func GroupDefinitions() []GroupInfo {
	return []GroupInfo{
		{"latin", "英语 / English（西文共用）", "英语、法语、德语、西班牙语、意大利语、葡萄牙语、波兰语、俄语等共用", "Helldivers 2 — Freedom! Liberté! Свобода!"},
		{"sc", "简体中文", "简体中文界面与备用字库", "为了超级地球！绝地潜兵，准备出击。"},
		{"tc", "繁體中文", "繁體中文介面與備用字庫", "為了超級地球！絕地戰兵，準備出擊。"},
		{"jp", "日本語", "日本語界面与备用字库", "スーパーアースのために！ヘルダイバー"},
		{"ko", "한국어", "韩文界面与备用字库", "슈퍼지구를 위하여! 헬다이버"},
		{"arabic", "العربية / 阿拉伯文", "备用字体；不增加游戏本身的翻译", "الحرية والديمقراطية"},
		{"bengali", "বাংলা / 孟加拉文", "备用字体；不增加游戏本身的翻译", "স্বাধীনতা গণতন্ত্র"},
		{"devanagari", "देवनागरी / 天城文", "印地语等备用字体；不增加翻译", "स्वतंत्रता और लोकतंत्र"},
		{"thai", "ไทย / 泰文", "备用字体；不增加游戏本身的翻译", "เสรีภาพและประชาธิปไตย"},
		{"emoji", "Emoji / 表情符号", "可单独设置，建议使用包含表情字形的字体", "☺ ♥ ★ ☀"},
		{"special", "装饰字 / 阵营文字", "只替换字体文件；独立预烘焙装饰贴图保留", "SUPER EARTH 0123456789"},
		{"other", "其他发现的字体", "新版本或未分类字体", "Helldivers 2"},
	}
}

type LanguageInfo struct {
	ID     string   `json:"id"`
	Label  string   `json:"label"`
	Groups []string `json:"groups"`
}

func LanguageDefinitions() []LanguageInfo {
	return []LanguageInfo{
		{"sc", "简体中文", []string{"latin", "sc"}},
		{"tc", "繁體中文", []string{"latin", "tc"}},
		{"latin", "英语 / 西文共用", []string{"latin"}},
		{"jp", "日本語", []string{"latin", "jp"}},
		{"ko", "한국어", []string{"latin", "ko"}},
	}
}

func DefaultLanguages() []string { return []string{"sc"} }

func ResourceLabel(group string) string {
	if group == "latin" {
		return "共用西文（英文、数字等）"
	}
	for _, g := range GroupDefinitions() {
		if g.ID == group {
			return g.Label
		}
	}
	return group
}

func NormalizeLanguages(languages []string) ([]string, error) {
	chosen := make(map[string]bool)
	for _, language := range languages {
		chosen[language] = true
	}
	if len(chosen) == 0 {
		return nil, fmt.Errorf("select at least one game language")
	}
	result := make([]string, 0, len(chosen))
	for _, language := range LanguageDefinitions() {
		if chosen[language.ID] {
			result = append(result, language.ID)
			delete(chosen, language.ID)
		}
	}
	if len(chosen) != 0 {
		var unknown []string
		for id := range chosen {
			unknown = append(unknown, id)
		}
		sort.Strings(unknown)
		return nil, fmt.Errorf("unsupported game languages: %s", strings.Join(unknown, ", "))
	}
	return result, nil
}

func ResourceGroups(languages []string) ([]string, error) {
	selected, err := NormalizeLanguages(languages)
	if err != nil {
		return nil, err
	}
	required := make(map[string]bool)
	for _, language := range LanguageDefinitions() {
		for _, id := range selected {
			if language.ID == id {
				for _, g := range language.Groups {
					required[g] = true
				}
			}
		}
	}
	var result []string
	for _, g := range GroupDefinitions() {
		if required[g.ID] {
			result = append(result, g.ID)
		}
	}
	return result, nil
}

type Selection struct {
	Path  string `json:"path"`
	Index int    `json:"index"`
}

type Coverage struct {
	Group                      string `json:"group"`
	Label                      string `json:"label"`
	Selected                   bool   `json:"selected"`
	OriginalCharacters         int    `json:"original_characters"`
	CustomCharacters           int    `json:"custom_characters"`
	OriginalFallbackCharacters int    `json:"original_fallback_characters"`
	Reason                     string `json:"reason"`
}

// LanguagePlan selects by the user's languages, never by incidental ASCII
// overlap. Nil languages uses the default; an explicit empty slice is an error.
func LanguagePlan(inv *Inventory, face *Face, path string, index int, languages []string, includeSymbols bool) (map[string]Selection, []Coverage, error) {
	if inv == nil || face == nil {
		return nil, nil, fmt.Errorf("inventory and custom face are required")
	}
	if index < 0 {
		return nil, nil, fmt.Errorf("negative font face index")
	}
	if languages == nil {
		languages = DefaultLanguages()
	}
	groups, err := ResourceGroups(languages)
	if err != nil {
		return nil, nil, err
	}
	required := make(map[string]bool)
	for _, group := range groups {
		if len(inv.Groups[group]) == 0 {
			return nil, nil, fmt.Errorf("required resource group %s is missing", group)
		}
		required[group] = true
	}
	if includeSymbols {
		for _, g := range []string{"emoji", "special"} {
			if len(inv.Groups[g]) > 0 {
				required[g] = true
			}
		}
	}
	resolved, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, err
	}
	selections := make(map[string]Selection)
	var coverage []Coverage
	for _, g := range GroupDefinitions() {
		originals := inv.Groups[g.ID]
		if len(originals) == 0 {
			continue
		}
		chars := make(map[rune]struct{})
		for _, original := range originals {
			for cp := range original.Chars {
				chars[cp] = struct{}{}
			}
		}
		covered := 0
		for cp := range chars {
			if _, ok := face.Chars[cp]; ok {
				covered++
			}
		}
		selected := required[g.ID]
		if (g.ID == "emoji" || g.ID == "special") && covered == 0 {
			selected = false
		}
		row := Coverage{Group: g.ID, Label: ResourceLabel(g.ID), Selected: selected, OriginalCharacters: len(chars), OriginalFallbackCharacters: len(chars), Reason: "outside_language_scope"}
		if selected {
			if covered == 0 {
				return nil, nil, fmt.Errorf("custom font has no matching glyphs for %s", g.ID)
			}
			selections[g.ID] = Selection{resolved, index}
			row.CustomCharacters = covered
			row.OriginalFallbackCharacters = len(chars) - covered
			row.Reason = "selected_language_or_shared_resource"
		}
		coverage = append(coverage, row)
	}
	return selections, coverage, nil
}
