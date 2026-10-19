package geometry

import (
	"fmt"
	"math"
)

type splitPoint [2]float32

type splitEdge struct {
	command Command
	from    splitPoint
	to      splitPoint
}

// SplitTouchingContours separates closed walks that revisit an edge endpoint
// into independent closed contours. Coordinates and curve commands are copied
// exactly; only zero-length straight edges are omitted and a missing closing
// straight edge is added. Crossing between distinct input contours is retained.
//
// The walk is decomposed with an endpoint stack, so the work is linear in the
// number of path segments and does not recurse.
func SplitTouchingContours(path Path) (Path, error) {
	var output Path
	for i := 0; i < len(path); {
		if path[i].Verb != Move {
			return nil, fmt.Errorf("command %d: contour must begin with Move", i)
		}
		if err := splitFinite(path[i], 2); err != nil {
			return nil, fmt.Errorf("command %d: %w", i, err)
		}

		start := splitPoint{path[i].Points[0], path[i].Points[1]}
		current := start
		edges := make([]splitEdge, 0, 8)
		i++
		closed := false
		for ; i < len(path); i++ {
			command := path[i]
			if command.Verb == Close {
				closed = true
				i++
				break
			}
			if command.Verb == Move {
				return nil, fmt.Errorf("command %d: move before closing contour", i)
			}
			n, next, err := splitEndpoint(command)
			if err != nil {
				return nil, fmt.Errorf("command %d: %w", i, err)
			}
			if err := splitFinite(command, n); err != nil {
				return nil, fmt.Errorf("command %d: %w", i, err)
			}
			if command.Verb == Line && current == next {
				continue
			}
			edges = append(edges, splitEdge{command: command, from: current, to: next})
			current = next
		}
		if !closed {
			return nil, fmt.Errorf("path ends before closing contour")
		}
		if current != start {
			edges = append(edges, splitEdge{
				command: Command{Verb: Line, Points: [6]float32{start[0], start[1]}},
				from:    current,
				to:      start,
			})
		}
		splitClosedWalk(&output, edges)
	}
	return output, nil
}

// splitClosedWalk extracts simple cycles as soon as an endpoint repeats. The
// remaining stack is the walk prefix that preceded the cycle. Each vertex and
// edge is pushed and popped at most once per traversal, keeping total work O(n).
func splitClosedWalk(output *Path, edges []splitEdge) {
	if len(edges) == 0 {
		return
	}

	vertices := make([]splitPoint, 0, len(edges)+1)
	vertices = append(vertices, edges[0].from)
	positions := map[splitPoint]int{edges[0].from: 0}
	walk := make([]splitEdge, 0, len(edges))
	for _, edge := range edges {
		walk = append(walk, edge)
		if index, found := positions[edge.to]; found {
			appendSplitContour(output, vertices[index], walk[index:])
			for _, vertex := range vertices[index+1:] {
				delete(positions, vertex)
			}
			vertices = vertices[:index+1]
			walk = walk[:index]
			continue
		}
		positions[edge.to] = len(vertices)
		vertices = append(vertices, edge.to)
	}
}

func appendSplitContour(output *Path, start splitPoint, edges []splitEdge) {
	*output = append(*output, Command{Verb: Move, Points: [6]float32{start[0], start[1]}})
	for _, edge := range edges {
		*output = append(*output, edge.command)
	}
	*output = append(*output, Command{Verb: Close})
}

func splitEndpoint(command Command) (int, splitPoint, error) {
	switch command.Verb {
	case Line:
		return 2, splitPoint{command.Points[0], command.Points[1]}, nil
	case Quad:
		return 4, splitPoint{command.Points[2], command.Points[3]}, nil
	case Cubic:
		return 6, splitPoint{command.Points[4], command.Points[5]}, nil
	default:
		return 0, splitPoint{}, fmt.Errorf("unknown verb %d", command.Verb)
	}
}

func splitFinite(command Command, count int) error {
	for _, value := range command.Points[:count] {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return fmt.Errorf("non-finite coordinate")
		}
	}
	return nil
}
