//go:build windows && amd64

package geometry

import (
	"fmt"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"unsafe"
)

type Options struct {
	Radius    float32
	PixelGrid bool
	Clockwise bool
	Simplify  bool
}

type Engine struct {
	mu            sync.RWMutex
	skia, bridge  *syscall.DLL
	process, free *syscall.Proc
}

// Open loads only the two explicitly supplied native libraries. The caller
// retains the engine for an entire job, allowing concurrent independent glyphs.
func Open(directory string) (*Engine, error) {
	directory, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	skia, err := syscall.LoadDLL(filepath.Join(directory, "skia.dll"))
	if err != nil {
		return nil, fmt.Errorf("load native Skia: %w", err)
	}
	bridge, err := syscall.LoadDLL(filepath.Join(directory, "hd2skia.dll"))
	if err != nil {
		skia.Release()
		return nil, fmt.Errorf("load native geometry bridge: %w", err)
	}
	e := &Engine{skia: skia, bridge: bridge}
	version, err := bridge.FindProc("hd_path_version")
	if err == nil {
		v, _, _ := version.Call()
		if v != 1 {
			err = fmt.Errorf("unsupported geometry ABI %d", v)
		}
	}
	if err == nil {
		e.process, err = bridge.FindProc("hd_path_process")
	}
	if err == nil {
		e.free, err = bridge.FindProc("hd_path_free")
	}
	if err != nil {
		e.Close()
		return nil, err
	}
	return e, nil
}

func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.bridge == nil {
		return nil
	}
	err := e.bridge.Release()
	e.bridge = nil
	if next := e.skia.Release(); err == nil {
		err = next
	}
	e.skia = nil
	return err
}

func (e *Engine) Process(path Path, options Options) (Path, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.bridge == nil {
		return nil, fmt.Errorf("geometry engine is closed")
	}
	flags := struct {
		Radius                         float32
		PixelGrid, Clockwise, Simplify uint32
	}{Radius: options.Radius}
	if options.PixelGrid {
		flags.PixelGrid = 1
	}
	if options.Clockwise {
		flags.Clockwise = 1
	}
	if options.Simplify {
		flags.Simplify = 1
	}
	result := struct {
		Commands unsafe.Pointer
		Count    uint64
	}{}
	var input unsafe.Pointer
	if len(path) > 0 {
		input = unsafe.Pointer(&path[0])
	}
	code, _, _ := e.process.Call(uintptr(input), uintptr(len(path)), uintptr(unsafe.Pointer(&flags)), uintptr(unsafe.Pointer(&result)))
	runtime.KeepAlive(path)
	defer e.free.Call(uintptr(unsafe.Pointer(&result)))
	if code != 0 {
		return nil, fmt.Errorf("native path operation failed (code %d)", code)
	}
	return append(Path(nil), unsafe.Slice((*Command)(result.Commands), int(result.Count))...), nil
}
