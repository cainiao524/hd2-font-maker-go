//go:build windows && amd64

package geometry

import (
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"unsafe"
)

func engine(t *testing.T) *Engine {
	t.Helper()
	e, err := Open(filepath.Join("..", "..", "bin"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

func rectangle(x, y, w, h float64) Path {
	var p Path
	p.MoveTo(x, y)
	p.LineTo(x+w, y)
	p.LineTo(x+w, y+h)
	p.LineTo(x, y+h)
	p.Close()
	return p
}

func TestNativeRoundtrip(t *testing.T) {
	if unsafe.Sizeof(Command{}) != 28 {
		t.Fatal("C ABI command size")
	}
	e := engine(t)
	p := rectangle(-10, -20, 100, 200)
	out, err := e.Process(p, Options{})
	if err != nil || !reflect.DeepEqual(p, out) {
		t.Fatalf("roundtrip: %v %v", out, err)
	}
	if _, err = e.Process(Path{{Verb: 8}}, Options{}); err == nil {
		t.Fatal("accepted invalid verb")
	}
	if _, err = e.Process(p, Options{Radius: -1}); err == nil {
		t.Fatal("accepted negative radius")
	}
	e.Close()
	if _, err = e.Process(p, Options{}); err == nil {
		t.Fatal("accepted closed engine")
	}
}

func TestNativeConcurrentStroke(t *testing.T) {
	e := engine(t)
	p := rectangle(0, 0, 100, 100)
	want, err := e.Process(p, Options{Radius: 12, Clockwise: true})
	if err != nil || len(want) == 0 {
		t.Fatalf("stroke: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			for j := 0; j < 20; j++ {
				got, err := e.Process(p, Options{Radius: 12, Clockwise: true})
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Errorf("nondeterministic stroke: %v", err)
					return
				}
			}
		})
	}
	wg.Wait()
}

func TestSimplifyKeepsImplicitClosingLine(t *testing.T) {
	e := engine(t)
	var p Path
	p.MoveTo(20, 0)
	p.LineTo(20, 700)
	p.LineTo(550, 700)
	p.LineTo(550, 0)
	p.Close()
	got, err := e.Process(p, Options{Clockwise: true, Simplify: true})
	if err != nil {
		t.Fatal(err)
	}
	if !EqualContours(p, got) {
		t.Fatalf("no-op normalization changed contour commands: %v", got)
	}
}
