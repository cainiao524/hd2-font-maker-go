package archiveio

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
)

// Resource contains original metadata and already-extracted replacement bytes.
// Builders copy metadata but borrow payload slices for the duration of the call.
type Resource struct {
	Entry  Entry
	Type   TypeRecord
	Main   []byte
	Stream []byte
	GPU    []byte
}

type Parts struct {
	Main   []byte
	Stream []byte
	GPU    []byte
}

type patchPlan struct {
	index     Index
	resources []Resource
	sizes     [3]uint64
	tableSize uint64
}

func align(position uint64, alignment uint32) uint64 {
	if alignment == 0 {
		alignment = 16
	}
	a := uint64(alignment)
	return position + (a-position%a)%a
}

func planPatch(resources []Resource, mainOnly bool) (patchPlan, error) {
	var plan patchPlan
	if mainOnly && len(resources) == 0 {
		return plan, fmt.Errorf("no resources to patch")
	}
	if uint64(len(resources)) > math.MaxUint32 {
		return plan, fmt.Errorf("too many resources")
	}
	plan.resources = append([]Resource(nil), resources...)
	sort.Slice(plan.resources, func(i, j int) bool {
		a, b := plan.resources[i].Entry, plan.resources[j].Entry
		if a.TypeID != b.TypeID {
			return a.TypeID < b.TypeID
		}
		return a.NameID < b.NameID
	})
	types := make(map[uint64]int)
	for i, resource := range plan.resources {
		e := resource.Entry
		if i > 0 {
			last := plan.resources[i-1].Entry
			if e.NameID == last.NameID && e.TypeID == last.TypeID {
				return plan, fmt.Errorf("duplicate resource (%#x,%#x)", e.NameID, e.TypeID)
			}
		}
		if resource.Type.TypeID != e.TypeID {
			return plan, fmt.Errorf("resource %#x type record mismatch", e.NameID)
		}
		if mainOnly && (e.StreamSize != 0 || e.GPUSize != 0 || len(resource.Stream) != 0 || len(resource.GPU) != 0) {
			return plan, fmt.Errorf("resource %#x contains unhandled external data", e.NameID)
		}
		for _, payload := range [][]byte{resource.Main, resource.Stream, resource.GPU} {
			if uint64(len(payload)) > math.MaxUint32 {
				return plan, fmt.Errorf("resource %#x payload exceeds uint32 size", e.NameID)
			}
		}
		j, exists := types[e.TypeID]
		if !exists {
			j = len(plan.index.Types)
			types[e.TypeID] = j
			t := resource.Type
			t.Count = 0
			plan.index.Types = append(plan.index.Types, t)
		}
		plan.index.Types[j].Count++
	}
	plan.tableSize = HeaderSize + uint64(len(plan.index.Types))*TypeSize + uint64(len(resources))*EntrySize
	plan.sizes[0] = plan.tableSize
	for i, resource := range plan.resources {
		e := resource.Entry
		e.Unknown0, e.Unknown1 = 0, 0
		payloads := [3][]byte{resource.Main, resource.Stream, resource.GPU}
		alignments := [3]uint32{e.MainAlignment, 16, e.GPUAlignment}
		var offsets [3]uint64
		for channel, payload := range payloads {
			plan.sizes[channel] = align(plan.sizes[channel], alignments[channel])
			if len(payload) != 0 || (mainOnly && channel == 0) {
				offsets[channel] = plan.sizes[channel]
			}
			plan.sizes[channel] += uint64(len(payload))
			if plan.sizes[channel] > math.MaxInt64 {
				return plan, fmt.Errorf("patch exceeds supported file size")
			}
		}
		e.MainOffset, e.StreamOffset, e.GPUOffset = offsets[0], offsets[1], offsets[2]
		e.MainSize, e.StreamSize, e.GPUSize = uint32(len(resource.Main)), uint32(len(resource.Stream)), uint32(len(resource.GPU))
		e.Index = uint32(i)
		plan.index.Entries = append(plan.index.Entries, e)
	}
	plan.sizes[0] = align(plan.sizes[0], 16)
	plan.index.Header = Header{Magic: Magic, TypeCount: uint32(len(plan.index.Types)), EntryCount: uint32(len(resources)), MainSize: plan.sizes[0], GPUSize: plan.sizes[2]}
	return plan, plan.index.ValidateBounds(plan.sizes)
}

func writeBytes(w io.Writer, data []byte) error {
	for len(data) != 0 {
		chunk := data[:min(len(data), 1024*1024)]
		n, err := w.Write(chunk)
		if err != nil {
			return err
		}
		if n != len(chunk) {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func writeZeros(w io.Writer, count uint64) error {
	var zeros [4096]byte
	for count != 0 {
		n := min(count, uint64(len(zeros)))
		if err := writeBytes(w, zeros[:n]); err != nil {
			return err
		}
		count -= n
	}
	return nil
}

func (plan patchPlan) write(writers [3]io.Writer) error {
	header, err := plan.index.Header.MarshalBinary()
	if err != nil {
		return err
	}
	if err = writeBytes(writers[0], header); err != nil {
		return err
	}
	for _, record := range plan.index.Types {
		data, err := record.MarshalBinary()
		if err != nil {
			return err
		}
		if err = writeBytes(writers[0], data); err != nil {
			return err
		}
	}
	for _, record := range plan.index.Entries {
		data, err := record.MarshalBinary()
		if err != nil {
			return err
		}
		if err = writeBytes(writers[0], data); err != nil {
			return err
		}
	}
	positions := [3]uint64{plan.tableSize, 0, 0}
	for _, resource := range plan.resources {
		payloads := [3][]byte{resource.Main, resource.Stream, resource.GPU}
		alignments := [3]uint32{resource.Entry.MainAlignment, 16, resource.Entry.GPUAlignment}
		for channel, payload := range payloads {
			aligned := align(positions[channel], alignments[channel])
			if err = writeZeros(writers[channel], aligned-positions[channel]); err != nil {
				return err
			}
			if err = writeBytes(writers[channel], payload); err != nil {
				return err
			}
			positions[channel] = aligned + uint64(len(payload))
		}
	}
	return writeZeros(writers[0], plan.sizes[0]-positions[0])
}

// MakePatch matches archives.make_patch, including a nonzero main offset for
// empty payloads. Original entries with external data are rejected.
func MakePatch(resources []Resource) ([]byte, error) {
	plan, err := planPatch(resources, true)
	if err != nil {
		return nil, err
	}
	var main bytes.Buffer
	if err = plan.write([3]io.Writer{&main, io.Discard, io.Discard}); err != nil {
		return nil, err
	}
	return main.Bytes(), nil
}

// MakePatchParts matches archives.make_patch_parts byte for byte. Empty input
// produces an aligned empty main archive and empty companion files.
func MakePatchParts(resources []Resource) (Parts, error) {
	plan, err := planPatch(resources, false)
	if err != nil {
		return Parts{}, err
	}
	var main, stream, gpu bytes.Buffer
	if err = plan.write([3]io.Writer{&main, &stream, &gpu}); err != nil {
		return Parts{}, err
	}
	return Parts{Main: main.Bytes(), Stream: stream.Bytes(), GPU: gpu.Bytes()}, nil
}

// WritePatchParts streams the exact MakePatchParts bytes to new files in
// main/stream/GPU order, then verifies the saved index and all companion bounds.
// Existing paths are never overwritten. Files created by a failed call are removed.
func WritePatchParts(resources []Resource, paths [3]string) (sizes [3]uint64, err error) {
	plan, err := planPatch(resources, false)
	if err != nil {
		return sizes, err
	}
	var files [3]*os.File
	defer func() {
		for _, f := range files {
			if f != nil {
				if closeErr := f.Close(); err == nil && closeErr != nil {
					err = closeErr
				}
			}
		}
		if err != nil {
			for i, f := range files {
				if f != nil {
					_ = os.Remove(paths[i])
				}
			}
		}
	}()
	var writers [3]io.Writer
	for i, path := range paths {
		files[i], err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if err != nil {
			return sizes, err
		}
		writers[i] = files[i]
	}
	if err = plan.write(writers); err != nil {
		return sizes, err
	}
	for i, f := range files {
		info, statErr := f.Stat()
		if statErr != nil {
			return sizes, statErr
		}
		sizes[i] = uint64(info.Size())
	}
	if sizes != plan.sizes {
		return sizes, fmt.Errorf("patch file sizes changed during writing")
	}
	reader, err := NewReader(files[0], sizes[0])
	if err != nil {
		return sizes, err
	}
	saved, err := ReadIndex(reader)
	if err != nil {
		return sizes, err
	}
	if saved.Header != plan.index.Header || len(saved.Types) != len(plan.index.Types) || len(saved.Entries) != len(plan.index.Entries) {
		return sizes, fmt.Errorf("saved archive index differs")
	}
	for i := range saved.Types {
		if saved.Types[i] != plan.index.Types[i] {
			return sizes, fmt.Errorf("saved archive type %d differs", i)
		}
	}
	for i := range saved.Entries {
		if saved.Entries[i] != plan.index.Entries[i] {
			return sizes, fmt.Errorf("saved archive entry %d differs", i)
		}
	}
	return sizes, saved.ValidateBounds(sizes)
}
