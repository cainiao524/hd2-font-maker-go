package pipeline

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"

	"hd2font/native/internal/archiveio"
	"hd2font/native/internal/inventory"
)

func resourceKey(r *inventory.Resource) string {
	return fmt.Sprintf("%s/%016x/%016x", r.Archive, r.Entry.TypeID, r.Entry.NameID)
}

func (s *buildState) put(r *inventory.Resource, main, stream, gpu []byte) error {
	key := resourceKey(r)
	if s.changed[key] != nil {
		return fmt.Errorf("resource is replaced twice: %s", key)
	}
	if len(r.MissingParts) > 0 && (r.Kind != "texture" || len(gpu) == 0) {
		return fmt.Errorf("missing original companions for %s were not replaced by a complete atlas", key)
	}
	if r.Kind != "texture" && (r.Entry.StreamSize != 0 || r.Entry.GPUSize != 0) {
		return fmt.Errorf("unsupported external data in font resource %s", key)
	}
	s.changes[r.Archive] = append(s.changes[r.Archive], archiveio.Resource{Entry: r.Entry, Type: r.TypeRecord, Main: main, Stream: stream, GPU: gpu})
	s.changed[key] = r
	s.report.Resources = append(s.report.Resources, ResourceReport{r.Archive, fmt.Sprintf("%016x", r.Entry.NameID), r.Kind, inventory.Digest(r.Payload), inventory.Digest(main), inventory.Digest(stream), inventory.Digest(gpu)})
	return nil
}

func (s *buildState) fontResources() error {
	for _, r := range s.inv.Resources {
		if err := s.ctx.Err(); err != nil {
			return err
		}
		switch r.Kind {
		case "ttf", "otf":
			if len(r.Faces) != 1 || len(r.Payload) < 16 {
				return fmt.Errorf("invalid wrapped font %s", resourceKey(r))
			}
			f := r.Faces[0]
			if _, ok := s.selected[f.Group]; !ok {
				continue
			}
			replacement, ok := s.replacements[f.Digest]
			if !ok {
				return fmt.Errorf("missing replacement for %s", f.Name)
			}
			if uint64(len(replacement.data)) > math.MaxUint32 {
				return fmt.Errorf("font payload exceeds uint32")
			}
			payload := make([]byte, 16+len(replacement.data))
			copy(payload, r.Payload[:16])
			binary.LittleEndian.PutUint32(payload, uint32(len(replacement.data)))
			copy(payload[16:], replacement.data)
			if err := s.put(r, payload, nil, nil); err != nil {
				return err
			}
		case "runtime_font":
			parts, err := archiveio.RuntimeParts(r.Payload)
			if err != nil {
				return err
			}
			if len(parts) != len(r.Faces) {
				return fmt.Errorf("runtime slot metadata mismatch")
			}
			row := RuntimeReport{Archive: r.Archive, ID: fmt.Sprintf("%016x", r.Entry.NameID), OriginalSlots: len(parts), OutputSlots: len(parts), ReplacedSlots: []int{}}
			for i, f := range r.Faces {
				if _, ok := s.selected[f.Group]; !ok {
					continue
				}
				replacement, ok := s.replacements[f.Digest]
				if !ok {
					return fmt.Errorf("missing runtime replacement for %s", f.Name)
				}
				parts[i] = archiveio.RuntimePart{Metrics: replacement.metrics, Data: replacement.data}
				row.ReplacedSlots = append(row.ReplacedSlots, i)
			}
			if len(row.ReplacedSlots) == 0 {
				continue
			}
			payload, err := archiveio.MakeRuntime(r.Payload, parts)
			if err != nil {
				return err
			}
			if err = s.put(r, payload, nil, nil); err != nil {
				return err
			}
			s.report.RuntimeSlots = append(s.report.RuntimeSlots, row)
		}
	}
	return nil
}

func (s *buildState) verifyFontPayload(r *inventory.Resource, payload []byte) error {
	check := func(original *inventory.Face, data []byte) error {
		replacement, ok := s.replacements[original.Digest]
		if !ok {
			return fmt.Errorf("readback missing replacement identity")
		}
		wantSize, err := archiveio.SFNTSize(replacement.data)
		if err != nil {
			return err
		}
		gotSize, err := archiveio.SFNTSize(data)
		if err != nil {
			return err
		}
		if !bytes.Equal(replacement.data[:wantSize], data[:gotSize]) {
			return fmt.Errorf("saved font bytes differ for %q", original.Name)
		}
		face, err := inventory.InspectFace(data)
		if err != nil {
			return err
		}
		return hasCoverage(face, replacement.required, "saved font readback")
	}
	switch r.Kind {
	case "ttf", "otf":
		if len(payload) < 16 || uint64(binary.LittleEndian.Uint32(payload))+16 != uint64(len(payload)) {
			return fmt.Errorf("saved wrapper size mismatch")
		}
		if !bytes.Equal(payload[4:16], r.Payload[4:16]) {
			return fmt.Errorf("saved wrapper metadata changed")
		}
		return check(r.Faces[0], payload[16:])
	case "runtime_font":
		old, err := archiveio.RuntimeParts(r.Payload)
		if err != nil {
			return err
		}
		parts, err := archiveio.RuntimeParts(payload)
		if err != nil {
			return err
		}
		if len(parts) != len(old) || len(parts) != len(r.Faces) || !bytes.Equal(payload[:32], r.Payload[:32]) {
			return fmt.Errorf("saved runtime slots/header changed")
		}
		for i, part := range parts {
			original := r.Faces[i]
			if _, ok := s.selected[original.Group]; !ok {
				if part.Metrics != old[i].Metrics || !bytes.Equal(part.Data, old[i].Data) {
					return fmt.Errorf("out-of-scope runtime slot %d changed", i)
				}
				continue
			}
			if part.Metrics != s.replacements[original.Digest].metrics {
				return fmt.Errorf("runtime slot %d metrics differ", i)
			}
			if err = check(original, part.Data); err != nil {
				return err
			}
		}
	}
	return nil
}
