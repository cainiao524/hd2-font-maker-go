package inventory

import (
	"fmt"

	"hd2font/native/internal/atlas"
)

var atlasGroups = []struct{ archive, group string }{
	{"09caa46bc556e38f", "sc"}, {"c0b7644cc5c4aaa8", "tc"},
	{"f7b9c03fff72ffd3", "jp"}, {"9212d7034dc5d55a", "ko"},
	{"0de4b81e19a79aa8", "latin"}, {"41c8eff8188e4862", "latin"},
}

func AtlasGroup(archive string) (string, bool) {
	for _, item := range atlasGroups {
		if item.archive == archive {
			return item.group, true
		}
	}
	return "", false
}

func AtlasGroups() map[string]string {
	result := make(map[string]string, len(atlasGroups))
	for _, item := range atlasGroups {
		result[item.archive] = item.group
	}
	return result
}

type AtlasSelection struct {
	Archive   string
	Group     string
	Baked     *Resource
	Texture   *Resource
	Template  *Face
	Originals []*Face
	Keys      []uint32
}

// SelectAtlases validates texture formats, counts, identity bindings and original
// advances for the requested resource groups. No selection is inferred from cmap.
func (inv *Inventory) SelectAtlases(groups []string) ([]AtlasSelection, error) {
	selected := make(map[string]bool)
	for _, g := range groups {
		selected[g] = true
	}
	var result []AtlasSelection
	for _, item := range atlasGroups {
		if !selected[item.group] {
			continue
		}
		var baked, textures []*Resource
		var originals []*Face
		seen := make(map[string]bool)
		add := func(f *Face) {
			if f.Group == item.group && !seen[f.Digest] {
				seen[f.Digest] = true
				originals = append(originals, f)
			}
		}
		for _, r := range inv.Resources {
			if r.Archive != item.archive {
				continue
			}
			switch r.Kind {
			case "font":
				baked = append(baked, r)
			case "texture":
				textures = append(textures, r)
			}
			for _, f := range r.Faces {
				add(f)
			}
		}
		if len(baked) == 0 {
			continue
		}
		for _, f := range inv.Groups[item.group] {
			add(f)
		}
		all := make([]atlas.Texture, len(textures))
		for i, r := range textures {
			all[i] = atlas.Texture{ID: r.Entry.NameID, Payload: r.Payload}
		}
		accepted, err := atlas.AtlasTextures(all)
		if err != nil {
			return nil, fmt.Errorf("archive %s: %w", item.archive, err)
		}
		if len(accepted) != len(baked) {
			return nil, fmt.Errorf("archive %s has %d baked fonts and %d atlas textures", item.archive, len(baked), len(accepted))
		}
		metrics := make([]atlas.FontMetrics, len(originals))
		for i, f := range originals {
			metrics[i] = atlas.FontMetrics{Name: f.Name, UnitsPerEm: f.UnitsPerEm, Advances: f.Advances}
		}
		owners := make(map[uint64]bool)
		for _, r := range baked {
			keys, err := atlas.GlyphKeys(r.Payload)
			if err != nil {
				return nil, err
			}
			fi, ti, err := atlas.ResolveBinding(r.Entry.NameID, r.Payload, accepted, metrics)
			if err != nil {
				return nil, err
			}
			id := accepted[ti].ID
			if owners[id] {
				return nil, fmt.Errorf("multiple baked fonts own atlas texture %016x", id)
			}
			owners[id] = true
			var texture *Resource
			for _, t := range textures {
				if t.Entry.NameID == id {
					texture = t
					break
				}
			}
			result = append(result, AtlasSelection{item.archive, item.group, r, texture, originals[fi], originals, keys})
		}
	}
	return result, nil
}
