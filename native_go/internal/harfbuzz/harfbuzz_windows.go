//go:build windows && amd64

// Package harfbuzz provides native font instancing, subsetting and drawing.
// It uses a C ABI through Windows syscalls and does not require cgo.
package harfbuzz

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"hd2font/native/internal/geometry"
	"hd2font/native/internal/sfntio"
)

var ErrClosed = errors.New("HarfBuzz engine or face is closed")

type Engine struct {
	mu                                      sync.RWMutex
	dll                                     *syscall.DLL
	instance, subset, remap, resultFree     *syscall.Proc
	faceCreate, faceDestroy, draw, pathFree *syscall.Proc
	shape, shapeFree                        *syscall.Proc
	faces                                   map[*Face]struct{}
	version                                 uint32
}

type Face struct {
	engine *Engine
	handle uintptr // Protected by engine.mu, including during native calls.
}

type axis struct {
	Tag   uint32
	Value float32
}
type mapping struct{ Old, New uint32 }
type nativeResult struct {
	Data         unsafe.Pointer
	Length       uint64
	Mapping      unsafe.Pointer
	MappingCount uint64
	Error        [256]byte
}
type nativeFace struct {
	Handle uintptr
	Error  [256]byte
}
type nativePath struct {
	Commands unsafe.Pointer
	Count    uint64
	Error    [256]byte
}

// Open loads the helper from an explicit directory, retaining it for a job.
func Open(binDir string) (*Engine, error) {
	directory, err := filepath.Abs(binDir)
	if err != nil {
		return nil, err
	}
	dll, err := syscall.LoadDLL(filepath.Join(directory, "hd2harfbuzz.dll"))
	if err != nil {
		return nil, fmt.Errorf("load native HarfBuzz: %w", err)
	}
	e := &Engine{dll: dll, faces: make(map[*Face]struct{})}
	fail := func(err error) (*Engine, error) { dll.Release(); return nil, err }
	version, err := dll.FindProc("hd_hb_version")
	if err != nil {
		return fail(err)
	}
	v, _, _ := version.Call()
	if v != 1 {
		return fail(fmt.Errorf("unsupported HarfBuzz ABI %d", v))
	}
	for _, entry := range []struct {
		name   string
		target **syscall.Proc
	}{
		{"hd_hb_instance", &e.instance}, {"hd_hb_subset", &e.subset},
		{"hd_hb_remap", &e.remap}, {"hd_hb_result_free", &e.resultFree},
		{"hd_hb_face_create", &e.faceCreate}, {"hd_hb_face_destroy", &e.faceDestroy},
		{"hd_hb_draw", &e.draw}, {"hd_hb_path_free", &e.pathFree},
		{"hd_hb_shape", &e.shape}, {"hd_hb_shape_free", &e.shapeFree},
	} {
		*entry.target, err = dll.FindProc(entry.name)
		if err != nil {
			return fail(err)
		}
	}
	sourceVersion, err := dll.FindProc("hd_hb_source_version")
	if err != nil {
		return fail(err)
	}
	v, _, _ = sourceVersion.Call()
	e.version = uint32(v)
	return e, nil
}

// Version returns the linked HarfBuzz version, such as "14.6.0".
func (e *Engine) Version() string {
	return fmt.Sprintf("%d.%d.%d", e.version/10000, e.version/100%100, e.version%100)
}

// Close waits for active operations, destroys remaining faces, and unloads the DLL.
func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.dll == nil {
		return nil
	}
	for face := range e.faces {
		e.faceDestroy.Call(face.handle)
		face.handle = 0
	}
	clear(e.faces)
	err := e.dll.Release()
	e.dll = nil
	return err
}

func nativeError(operation string, message [256]byte) error {
	end := bytes.IndexByte(message[:], 0)
	if end < 0 {
		end = len(message)
	}
	return fmt.Errorf("HarfBuzz %s: %s", operation, message[:end])
}

func resultBytes(out *nativeResult) []byte {
	return append([]byte(nil), unsafe.Slice((*byte)(out.Data), int(out.Length))...)
}

// Instance selects a face, pins every axis to its default or supplied location,
// and converts CFF2 to CFF1. Unknown tags and non-finite values are errors.
// HarfBuzz clamps values outside an axis's supported range.
func (e *Engine) Instance(data []byte, index uint32, axisValues map[string]float32) ([]byte, error) {
	keys := make([]string, 0, len(axisValues))
	for tag := range axisValues {
		keys = append(keys, tag)
	}
	sort.Strings(keys)
	axes := make([]axis, 0, len(keys))
	for _, tag := range keys {
		if len(tag) != 4 {
			return nil, fmt.Errorf("axis tag %q must contain four bytes", tag)
		}
		var value uint32
		for i := 0; i < 4; i++ {
			if tag[i] < 0x20 || tag[i] > 0x7e {
				return nil, fmt.Errorf("axis tag %q is not ASCII", tag)
			}
			value = value<<8 | uint32(tag[i])
		}
		axes = append(axes, axis{value, axisValues[tag]})
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.dll == nil {
		return nil, ErrClosed
	}
	var out nativeResult
	code, _, _ := e.instance.Call(uintptr(unsafe.Pointer(unsafe.SliceData(data))), uintptr(len(data)), uintptr(index),
		uintptr(unsafe.Pointer(unsafe.SliceData(axes))), uintptr(len(axes)), uintptr(unsafe.Pointer(&out)))
	runtime.KeepAlive(data)
	runtime.KeepAlive(axes)
	defer e.resultFree.Call(uintptr(unsafe.Pointer(&out)))
	if code != 0 {
		return nil, nativeError("instance", out.Error)
	}
	return resultBytes(&out), nil
}

// Subset retains requested source cmap codes, .notdef, glyphs 1 and 2 when present,
// and glyph closure. Compatibility glyphs do not add Unicode cmap entries.
// Codes are opaque integers in 0..0x10ffff, including legacy surrogate codes;
// this API does not interpret them as text or require valid Unicode scalars.
// All layout feature and script tags participate in the subset operation.
// Use Instance first to select a collection face or fully instantiate variations.
func (e *Engine) Subset(data []byte, codepoints []uint32) ([]byte, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.dll == nil {
		return nil, ErrClosed
	}
	var out nativeResult
	code, _, _ := e.subset.Call(uintptr(unsafe.Pointer(unsafe.SliceData(data))), uintptr(len(data)), uintptr(unsafe.Pointer(unsafe.SliceData(codepoints))),
		uintptr(len(codepoints)), uintptr(unsafe.Pointer(&out)))
	runtime.KeepAlive(data)
	runtime.KeepAlive(codepoints)
	defer e.resultFree.Call(uintptr(unsafe.Pointer(&out)))
	if code != 0 {
		return nil, nativeError("subset", out.Error)
	}
	font, err := sfntio.Parse(resultBytes(&out), 0)
	if err != nil {
		return nil, err
	}
	cmap := make(map[rune]uint16, out.MappingCount)
	for _, entry := range unsafe.Slice((*mapping)(out.Mapping), int(out.MappingCount)) {
		cmap[rune(entry.Old)] = uint16(entry.New)
	}
	font.Tables["cmap"] = sfntio.UnicodeCmap(cmap)
	return font.Bytes()
}

// Remap selects explicitly mapped source glyphs and adds .notdef and closure.
// Missing closure glyphs receive IDs above the highest requested destination.
// Glyph zero must remain zero; duplicate destinations and IDs >=65535 fail.
// The completed mapping must be monotonic because HarfBuzz does not guarantee
// sorted layout Coverage tables for arbitrary glyph permutations.
func (e *Engine) Remap(data []byte, oldToNew map[uint32]uint32) ([]byte, map[uint32]uint32, error) {
	entries := make([]mapping, 0, len(oldToNew))
	for old, next := range oldToNew {
		entries = append(entries, mapping{old, next})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Old < entries[j].Old })
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.dll == nil {
		return nil, nil, ErrClosed
	}
	var out nativeResult
	code, _, _ := e.remap.Call(uintptr(unsafe.Pointer(unsafe.SliceData(data))), uintptr(len(data)), uintptr(unsafe.Pointer(unsafe.SliceData(entries))),
		uintptr(len(entries)), uintptr(unsafe.Pointer(&out)))
	runtime.KeepAlive(data)
	runtime.KeepAlive(entries)
	defer e.resultFree.Call(uintptr(unsafe.Pointer(&out)))
	if code != 0 {
		return nil, nil, nativeError("remap", out.Error)
	}
	actual := make(map[uint32]uint32, out.MappingCount)
	for _, entry := range unsafe.Slice((*mapping)(out.Mapping), int(out.MappingCount)) {
		actual[entry.Old] = entry.New
	}
	return resultBytes(&out), actual, nil
}

// NewFace copies data into native-owned memory once. Concurrent Draw calls
// share the immutable face and native font without recopying the source data.
func (e *Engine) NewFace(data []byte, index uint32) (*Face, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.dll == nil {
		return nil, ErrClosed
	}
	var out nativeFace
	code, _, _ := e.faceCreate.Call(uintptr(unsafe.Pointer(unsafe.SliceData(data))), uintptr(len(data)), uintptr(index), uintptr(unsafe.Pointer(&out)))
	runtime.KeepAlive(data)
	if code != 0 {
		return nil, nativeError("face", out.Error)
	}
	f := &Face{engine: e, handle: out.Handle}
	e.faces[f] = struct{}{}
	return f, nil
}

func (f *Face) Close() error {
	e := f.engine
	e.mu.Lock()
	defer e.mu.Unlock()
	if f.handle != 0 {
		e.faceDestroy.Call(f.handle)
		f.handle = 0
		delete(e.faces, f)
	}
	return nil
}

// Draw returns y-up outlines scaled to upm. Zero keeps the source UPM.
// Empty glyphs return an empty path. Invalid or unreadable glyphs fail.
func (f *Face) Draw(gid uint32, upm uint32) (geometry.Path, error) {
	e := f.engine
	e.mu.RLock()
	defer e.mu.RUnlock()
	if f.handle == 0 || e.dll == nil {
		return nil, ErrClosed
	}
	var out nativePath
	code, _, _ := e.draw.Call(f.handle, uintptr(gid), uintptr(upm), uintptr(unsafe.Pointer(&out)))
	defer e.pathFree.Call(uintptr(unsafe.Pointer(&out)))
	if code != 0 {
		return nil, nativeError("draw", out.Error)
	}
	return append(geometry.Path(nil), unsafe.Slice((*geometry.Command)(out.Commands), int(out.Count))...), nil
}

// NativeDraw is the one-glyph convenience operation. Reuse NewFace for a font job.
func (e *Engine) NativeDraw(data []byte, gid uint32, upm uint32) (geometry.Path, error) {
	f, err := e.NewFace(data, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Draw(gid, upm)
}

// ShapeOptions uses ISO 15924 script tags, BCP 47 language tags, and HarfBuzz
// feature syntax ("liga=0", "kern=1"). Empty properties are inferred from text.
// Direction accepts "ltr", "rtl", "ttb", "btt". UPM zero uses the font scale.
type ShapeOptions struct {
	Script, Language, Direction string
	Features                    []string
	UPM                         uint32
}

// Glyph contains a glyph ID, UTF-8 byte cluster, and positions in font units.
type Glyph struct {
	ID, Cluster                          uint32
	XAdvance, YAdvance, XOffset, YOffset int32
}
type nativeShapeInput struct {
	Text                                  unsafe.Pointer
	Length                                uint64
	Script, Language, Direction, Features *byte
	UPM, Reserved                         uint32
}
type nativeShapeResult struct {
	Glyphs unsafe.Pointer
	Count  uint64
	Error  [256]byte
}

// Shape applies OpenType layout using the face's shared immutable native font.
func (f *Face) Shape(text string, options ShapeOptions) ([]Glyph, error) {
	input := nativeShapeInput{Text: unsafe.Pointer(unsafe.StringData(text)), Length: uint64(len(text)), UPM: options.UPM}
	for _, item := range []struct {
		value  string
		target **byte
	}{
		{options.Script, &input.Script}, {options.Language, &input.Language},
		{options.Direction, &input.Direction}, {strings.Join(options.Features, ","), &input.Features},
	} {
		ptr, err := syscall.BytePtrFromString(item.value)
		if err != nil {
			return nil, fmt.Errorf("shaping option contains NUL: %w", err)
		}
		*item.target = ptr
	}
	e := f.engine
	e.mu.RLock()
	defer e.mu.RUnlock()
	if f.handle == 0 || e.dll == nil {
		return nil, ErrClosed
	}
	var out nativeShapeResult
	code, _, _ := e.shape.Call(f.handle, uintptr(unsafe.Pointer(&input)), uintptr(unsafe.Pointer(&out)))
	runtime.KeepAlive(input)
	runtime.KeepAlive(text)
	defer e.shapeFree.Call(uintptr(unsafe.Pointer(&out)))
	if code != 0 {
		return nil, nativeError("shape", out.Error)
	}
	return append([]Glyph(nil), unsafe.Slice((*Glyph)(out.Glyphs), int(out.Count))...), nil
}

// Shape is a convenience for one shaping operation; reuse NewFace for a job.
func (e *Engine) Shape(data []byte, text string, options ShapeOptions) ([]Glyph, error) {
	f, err := e.NewFace(data, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Shape(text, options)
}
