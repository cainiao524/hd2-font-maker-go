package geometry

// Verb numbers are the stable C ABI, not Skia's internal enum.
const (
	Move uint32 = iota
	Line
	Quad
	Cubic
	Close
)

type Command struct {
	Verb   uint32
	Points [6]float32
}

type Path []Command

func (p *Path) MoveTo(x, y float64) {
	*p = append(*p, Command{Verb: Move, Points: [6]float32{float32(x), float32(y)}})
}
func (p *Path) LineTo(x, y float64) {
	*p = append(*p, Command{Verb: Line, Points: [6]float32{float32(x), float32(y)}})
}
func (p *Path) QuadTo(x1, y1, x2, y2 float64) {
	*p = append(*p, Command{Verb: Quad, Points: [6]float32{float32(x1), float32(y1), float32(x2), float32(y2)}})
}
func (p *Path) CubeTo(x1, y1, x2, y2, x3, y3 float64) {
	*p = append(*p, Command{Verb: Cubic, Points: [6]float32{float32(x1), float32(y1), float32(x2), float32(y2), float32(x3), float32(y3)}})
}
func (p *Path) Close() { *p = append(*p, Command{Verb: Close}) }
