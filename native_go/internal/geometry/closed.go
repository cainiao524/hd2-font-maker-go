package geometry

// ClosedSegments makes implicit closing edges explicit for contour comparison.
func ClosedSegments(path Path) Path {
	var out, contour Path
	var first, last [2]float32
	for _, c := range path {
		switch c.Verb {
		case Move:
			contour = append(contour[:0], c)
			first = [2]float32{c.Points[0], c.Points[1]}
			last = first
		case Close:
			if len(contour) > 1 {
				if last != first {
					contour.LineTo(float64(first[0]), float64(first[1]))
				}
				contour.Close()
				out = append(out, contour...)
			}
			contour = contour[:0]
		default:
			contour = append(contour, c)
			switch c.Verb {
			case Line:
				last = [2]float32{c.Points[0], c.Points[1]}
			case Quad:
				last = [2]float32{c.Points[2], c.Points[3]}
			case Cubic:
				last = [2]float32{c.Points[4], c.Points[5]}
			}
		}
	}
	return out
}
