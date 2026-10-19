package cffconv

import (
	"encoding/binary"
	"fmt"

	"hd2font/native/internal/geometry"
)

// DecodeLiteral reads this package's unhinted, unsubroutinized Type 2 output.
// It is used to clean the actual integer grid, not the pre-encoding geometry.
func DecodeLiteral(data []byte) (geometry.Path, error) {
	var path geometry.Path
	var stack []float64
	x, y := 0., 0.
	open := false
	first := true
	for i := 0; i < len(data); {
		b := data[i]
		i++
		if b >= 32 {
			var v float64
			switch {
			case b <= 246:
				v = float64(int(b) - 139)
			case b <= 250:
				if i >= len(data) {
					return nil, fmt.Errorf("truncated Type 2 number")
				}
				v = float64((int(b)-247)*256 + int(data[i]) + 108)
				i++
			case b <= 254:
				if i >= len(data) {
					return nil, fmt.Errorf("truncated Type 2 number")
				}
				v = float64(-(int(b)-251)*256 - int(data[i]) - 108)
				i++
			default:
				if i+4 > len(data) {
					return nil, fmt.Errorf("truncated fixed number")
				}
				v = float64(int32(binary.BigEndian.Uint32(data[i:]))) / 65536
				i += 4
			}
			stack = append(stack, v)
			continue
		}
		if b == 28 {
			if i+2 > len(data) {
				return nil, fmt.Errorf("truncated short number")
			}
			stack = append(stack, float64(int16(binary.BigEndian.Uint16(data[i:]))))
			i += 2
			continue
		}
		switch b {
		case 21:
			if first && len(stack) == 3 {
				stack = stack[1:]
			}
			if len(stack) != 2 {
				return nil, fmt.Errorf("invalid move operands")
			}
			if open {
				path.Close()
			}
			x += stack[0]
			y += stack[1]
			path.MoveTo(x, y)
			open = true
			first = false
		case 5:
			if !open || len(stack)%2 != 0 {
				return nil, fmt.Errorf("invalid line operands")
			}
			for j := 0; j < len(stack); j += 2 {
				x += stack[j]
				y += stack[j+1]
				path.LineTo(x, y)
			}
		case 8:
			if !open || len(stack)%6 != 0 {
				return nil, fmt.Errorf("invalid curve operands")
			}
			for j := 0; j < len(stack); j += 6 {
				x1, y1 := x+stack[j], y+stack[j+1]
				x2, y2 := x1+stack[j+2], y1+stack[j+3]
				x, y = x2+stack[j+4], y2+stack[j+5]
				path.CubeTo(x1, y1, x2, y2, x, y)
			}
		case 14:
			if open {
				path.Close()
			}
			if i != len(data) {
				return nil, fmt.Errorf("trailing Type 2 bytes")
			}
			return path, nil
		default:
			return nil, fmt.Errorf("unexpected literal Type 2 operator %d", b)
		}
		stack = stack[:0]
	}
	return nil, fmt.Errorf("Type 2 program has no endchar")
}
