package inventory

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"hd2font/native/internal/archiveio"
)

const SnapshotVersion = 1
const snapshotFormat = "hd2font-inventory"

// Manifest is a portable interchange format. Blob fields are lowercase SHA-256
// identities, resolved only as blobs/<digest>.bin, never filesystem paths.
type Manifest struct {
	Format    string             `json:"format"`
	Version   int                `json:"version"`
	Faces     []ManifestFace     `json:"faces"`
	Resources []ManifestResource `json:"resources"`
}

type ManifestFace struct {
	Name   string `json:"name"`
	Group  string `json:"group"`
	Digest string `json:"digest"`
	Chars  []rune `json:"chars"`
	Blob   string `json:"blob"`
}

type ManifestResource struct {
	Archive      string               `json:"archive"`
	Entry        archiveio.Entry      `json:"entry"`
	TypeRecord   archiveio.TypeRecord `json:"type_record"`
	Kind         string               `json:"kind"`
	Payload      string               `json:"payload"`
	Stream       string               `json:"stream,omitempty"`
	GPU          string               `json:"gpu,omitempty"`
	Faces        []string             `json:"faces"`
	MissingParts []string             `json:"missing_parts,omitempty"`
}

func Save(inv *Inventory, directory string) error {
	if inv == nil || len(inv.Faces) == 0 {
		return fmt.Errorf("cannot save an empty font inventory")
	}
	if err := os.MkdirAll(filepath.Join(directory, "blobs"), 0755); err != nil {
		return err
	}
	manifest := Manifest{Format: snapshotFormat, Version: SnapshotVersion}
	writeBlob := func(data []byte) (string, error) {
		if len(data) == 0 {
			return "", nil
		}
		digest := Digest(data)
		return digest, os.WriteFile(filepath.Join(directory, "blobs", digest+".bin"), data, 0644)
	}
	for _, f := range inv.OrderedFaces() {
		if f == nil || inv.Faces[f.Digest] != f || Digest(f.Data) != f.Digest {
			return fmt.Errorf("inconsistent face identity")
		}
		blob, err := writeBlob(f.Data)
		if err != nil {
			return err
		}
		manifest.Faces = append(manifest.Faces, ManifestFace{f.Name, f.Group, f.Digest, f.Codepoints(), blob})
	}
	for _, r := range inv.Resources {
		if err := validateResource(r, inv.Faces); err != nil {
			return err
		}
		m := ManifestResource{Archive: r.Archive, Entry: r.Entry, TypeRecord: r.TypeRecord, Kind: r.Kind, MissingParts: append([]string(nil), r.MissingParts...)}
		for _, part := range []struct {
			data   []byte
			target *string
		}{{r.Payload, &m.Payload}, {r.Stream, &m.Stream}, {r.GPU, &m.GPU}} {
			blob, err := writeBlob(part.data)
			if err != nil {
				return err
			}
			*part.target = blob
		}
		for _, f := range r.Faces {
			m.Faces = append(m.Faces, f.Digest)
		}
		manifest.Resources = append(manifest.Resources, m)
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(directory, ".manifest-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(append(data, '\n'))
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), filepath.Join(directory, "manifest.json"))
}

func (inv *Inventory) Save(directory string) error { return Save(inv, directory) }

func Load(directory string) (*Inventory, error) {
	data, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err = json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("inventory manifest: %w", err)
	}
	if m.Format != snapshotFormat || m.Version != SnapshotVersion {
		return nil, fmt.Errorf("unsupported inventory snapshot %q version %d", m.Format, m.Version)
	}
	cache := make(map[string][]byte)
	readBlob := func(digest string) ([]byte, error) {
		if digest == "" {
			return nil, nil
		}
		if data, ok := cache[digest]; ok {
			return data, nil
		}
		decoded, err := hex.DecodeString(digest)
		if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != digest {
			return nil, fmt.Errorf("invalid snapshot blob identity %q", digest)
		}
		data, err := os.ReadFile(filepath.Join(directory, "blobs", digest+".bin"))
		if err != nil {
			return nil, fmt.Errorf("snapshot blob %s: %w", digest, err)
		}
		if Digest(data) != digest {
			return nil, fmt.Errorf("snapshot blob %s failed SHA-256 verification", digest)
		}
		cache[digest] = data
		return data, nil
	}
	inv := &Inventory{Faces: make(map[string]*Face)}
	for _, row := range m.Faces {
		if inv.Faces[row.Digest] != nil {
			return nil, fmt.Errorf("duplicate snapshot face %s", row.Digest)
		}
		data, err := readBlob(row.Blob)
		if err != nil {
			return nil, err
		}
		face, err := InspectFace(data)
		if err != nil {
			return nil, err
		}
		if face.Digest != row.Digest || row.Blob != row.Digest || face.Name != row.Name || face.Group != row.Group || !reflect.DeepEqual(face.Codepoints(), row.Chars) {
			return nil, fmt.Errorf("snapshot face metadata does not match bytes: %s", row.Name)
		}
		inv.Faces[face.Digest] = face
		inv.FaceOrder = append(inv.FaceOrder, face.Digest)
	}
	if len(inv.Faces) == 0 {
		return nil, fmt.Errorf("snapshot has no fonts")
	}
	for _, row := range m.Resources {
		r := &Resource{Archive: row.Archive, Entry: row.Entry, TypeRecord: row.TypeRecord, Kind: row.Kind, MissingParts: row.MissingParts}
		for _, part := range []struct {
			blob   string
			target *[]byte
		}{{row.Payload, &r.Payload}, {row.Stream, &r.Stream}, {row.GPU, &r.GPU}} {
			data, err := readBlob(part.blob)
			if err != nil {
				return nil, err
			}
			*part.target = data
		}
		for _, digest := range row.Faces {
			f := inv.Faces[digest]
			if f == nil {
				return nil, fmt.Errorf("resource references missing face %s", digest)
			}
			r.Faces = append(r.Faces, f)
		}
		if err := validateResource(r, inv.Faces); err != nil {
			return nil, err
		}
		inv.Resources = append(inv.Resources, r)
	}
	inv.rebuildGroups()
	return inv, nil
}

func validateResource(r *Resource, faces map[string]*Face) error {
	if r == nil {
		return fmt.Errorf("nil inventory resource")
	}
	if len(r.Archive) != 16 {
		return fmt.Errorf("invalid resource archive %q", r.Archive)
	}
	if _, err := hex.DecodeString(r.Archive); err != nil {
		return fmt.Errorf("invalid resource archive: %w", err)
	}
	if r.Entry.TypeID != r.TypeRecord.TypeID || resourceKinds[r.Entry.TypeID] != r.Kind {
		return fmt.Errorf("inconsistent type metadata for resource %016x", r.Entry.NameID)
	}
	missing := make(map[string]bool)
	for _, part := range r.MissingParts {
		if (part != "stream" && part != "gpu") || missing[part] {
			return fmt.Errorf("invalid missing companion %q", part)
		}
		missing[part] = true
	}
	for _, part := range []struct {
		name string
		data []byte
		size uint32
	}{{"main", r.Payload, r.Entry.MainSize}, {"stream", r.Stream, r.Entry.StreamSize}, {"gpu", r.GPU, r.Entry.GPUSize}} {
		if missing[part.name] {
			if part.size == 0 || len(part.data) != 0 {
				return fmt.Errorf("inconsistent unavailable %s companion", part.name)
			}
		} else if uint64(len(part.data)) != uint64(part.size) {
			return fmt.Errorf("resource %016x %s payload length mismatch", r.Entry.NameID, part.name)
		}
	}
	raw, err := resourceFaces(r.Kind, r.Payload)
	if err != nil {
		return err
	}
	if len(raw) != len(r.Faces) {
		return fmt.Errorf("resource face count mismatch")
	}
	for i, data := range raw {
		size, err := archiveio.SFNTSize(data)
		if err != nil {
			return err
		}
		f := r.Faces[i]
		if f == nil || faces[f.Digest] != f || Digest(data[:size]) != f.Digest {
			return fmt.Errorf("resource face identity mismatch")
		}
	}
	return nil
}

// Complete reports whether every retained stream/GPU payload is available.
// Legacy pickle exports can still rebuild atlas replacements when false.
func (inv *Inventory) Complete() bool {
	for _, r := range inv.Resources {
		if len(r.MissingParts) > 0 {
			return false
		}
	}
	return true
}
