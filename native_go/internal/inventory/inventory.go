package inventory

import (
	"encoding/binary"
	"fmt"
	"sort"

	"hd2font/native/internal/archiveio"
	"hd2font/native/internal/catalog"
)

type Resource struct {
	Archive    string
	Entry      archiveio.Entry
	TypeRecord archiveio.TypeRecord
	Kind       string
	Payload    []byte
	Stream     []byte
	GPU        []byte
	Faces      []*Face
	// MissingParts is only used by legacy saved-inventory exports whose pickle
	// never contained the original stream/GPU bytes. Scan always leaves it empty.
	MissingParts []string
}

// Catalog is nil after Load. Resources retain all selected payloads and
// companions so an offline inventory never needs to reopen the installation.
// FaceOrder preserves first encounter order, including fallback precedence.
type Inventory struct {
	Catalog   *catalog.Catalog
	Resources []*Resource
	Faces     map[string]*Face
	Groups    map[string][]*Face
	FaceOrder []string
}

var resourceKinds = func() map[uint64]string {
	m := make(map[uint64]string)
	for _, name := range []string{"ttf", "otf", "runtime_font", "font", "texture"} {
		m[archiveio.ResourceHash(name)] = name
	}
	return m
}()

func Scan(gamePath string, progress func(string)) (*Inventory, error) {
	c, err := catalog.Open(gamePath)
	if err != nil {
		return nil, err
	}
	return ScanCatalog(c, progress)
}

func ScanCatalog(c *catalog.Catalog, progress func(string)) (*Inventory, error) {
	if c == nil {
		return nil, fmt.Errorf("nil catalog")
	}
	if progress == nil {
		progress = func(string) {}
	}
	inv := &Inventory{Catalog: c, Faces: make(map[string]*Face)}
	names := c.ArchiveNames()
	for i, name := range names {
		if i%100 == 0 {
			progress(fmt.Sprintf("Reading font index %d/%d", i, len(names)))
		}
		index, err := c.Resources(name)
		if err != nil {
			return nil, err
		}
		types := make(map[uint64]archiveio.TypeRecord, len(index.Types))
		for _, typ := range index.Types {
			types[typ.TypeID] = typ
		}
		_, isAtlas := AtlasGroup(name)
		for _, e := range index.Entries {
			kind := resourceKinds[e.TypeID]
			if kind != "ttf" && kind != "otf" && kind != "runtime_font" && !(isAtlas && (kind == "font" || kind == "texture")) {
				continue
			}
			typ, ok := types[e.TypeID]
			if !ok {
				return nil, fmt.Errorf("archive %s resource %016x has no type record", name, e.NameID)
			}
			payload, stream, gpu, err := c.ReadParts(name, e)
			if err != nil {
				return nil, err
			}
			res := &Resource{Archive: name, Entry: e, TypeRecord: typ, Kind: kind, Payload: payload, Stream: stream, GPU: gpu}
			rawFaces, err := resourceFaces(kind, payload)
			if err != nil {
				return nil, fmt.Errorf("archive %s resource %016x: %w", name, e.NameID, err)
			}
			for _, raw := range rawFaces {
				size, err := archiveio.SFNTSize(raw)
				if err != nil {
					return nil, err
				}
				digest := Digest(raw[:size])
				face := inv.Faces[digest]
				if face == nil {
					face, err = InspectFace(raw)
					if err != nil {
						return nil, err
					}
					inv.Faces[digest] = face
					inv.FaceOrder = append(inv.FaceOrder, digest)
				}
				res.Faces = append(res.Faces, face)
			}
			inv.Resources = append(inv.Resources, res)
		}
	}
	if len(inv.Faces) == 0 {
		return nil, fmt.Errorf("no recognizable font resources found")
	}
	inv.rebuildGroups()
	progress(fmt.Sprintf("Found %d unique fonts in %d resource groups", len(inv.Faces), len(inv.Groups)))
	return inv, nil
}

func resourceFaces(kind string, payload []byte) ([][]byte, error) {
	switch kind {
	case "ttf", "otf":
		if len(payload) < 16 || uint64(binary.LittleEndian.Uint32(payload))+16 != uint64(len(payload)) {
			return nil, fmt.Errorf("unsupported wrapped font length")
		}
		return [][]byte{payload[16:]}, nil
	case "runtime_font":
		parts, err := archiveio.RuntimeParts(payload)
		if err != nil {
			return nil, err
		}
		raw := make([][]byte, len(parts))
		for i, part := range parts {
			raw[i] = part.Data
		}
		return raw, nil
	case "font", "texture":
		return nil, nil
	default:
		return nil, fmt.Errorf("unsupported resource kind %q", kind)
	}
}

func (inv *Inventory) OrderedFaces() []*Face {
	result := make([]*Face, 0, len(inv.Faces))
	seen := make(map[string]bool)
	for _, digest := range inv.FaceOrder {
		if face := inv.Faces[digest]; face != nil && !seen[digest] {
			result = append(result, face)
			seen[digest] = true
		}
	}
	var remaining []string
	for digest := range inv.Faces {
		if !seen[digest] {
			remaining = append(remaining, digest)
		}
	}
	sort.Strings(remaining)
	for _, digest := range remaining {
		result = append(result, inv.Faces[digest])
	}
	return result
}

func (inv *Inventory) rebuildGroups() {
	inv.Groups = make(map[string][]*Face)
	for _, face := range inv.OrderedFaces() {
		inv.Groups[face.Group] = append(inv.Groups[face.Group], face)
	}
}
