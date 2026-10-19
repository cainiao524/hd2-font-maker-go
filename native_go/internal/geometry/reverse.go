package geometry

import "fmt"

// Reverse preserves each contour's start point and reverses controls and edges.
func Reverse(path Path) (Path, error) {
	var out Path
	for start := 0; start < len(path); {
		if path[start].Verb != Move {
			return nil, fmt.Errorf("contour must begin with Move")
		}
		end := start + 1
		for end < len(path) && path[end].Verb != Close {
			if path[end].Verb == Move {
				return nil, fmt.Errorf("unclosed contour")
			}
			end++
		}
		if end == len(path) {
			return nil, fmt.Errorf("unclosed contour")
		}
		first := [2]float32{path[start].Points[0], path[start].Points[1]}
		current := first
		starts := make([][2]float32, end-start)
		for i := start + 1; i < end; i++ {
			starts[i-start] = current
			switch path[i].Verb {
			case Line:
				current = [2]float32{path[i].Points[0], path[i].Points[1]}
			case Quad:
				current = [2]float32{path[i].Points[2], path[i].Points[3]}
			case Cubic:
				current = [2]float32{path[i].Points[4], path[i].Points[5]}
			default:
				return nil, fmt.Errorf("invalid contour verb")
			}
		}
		out = append(out, path[start])
		if current != first {
			out.LineTo(float64(current[0]), float64(current[1]))
		}
		for i := end - 1; i > start; i-- {
			c := path[i]
			p := starts[i-start]
			switch c.Verb {
			case Line:
				if i != start+1 {
					out.LineTo(float64(p[0]), float64(p[1]))
				}
			case Quad:
				out.QuadTo(float64(c.Points[0]), float64(c.Points[1]), float64(p[0]), float64(p[1]))
			case Cubic:
				out.CubeTo(float64(c.Points[2]), float64(c.Points[3]), float64(c.Points[0]), float64(c.Points[1]), float64(p[0]), float64(p[1]))
			}
		}
		out.Close()
		start = end + 1
	}
	return out, nil
}
