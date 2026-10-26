package pipeline

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"sort"

	"hd2font/native/internal/inventory"
	"hd2font/native/internal/sfntio"
)

// prepareFamily prepares every Options.Family face as the source was
// prepared, so each weight can use the family's own drawing. Each face keeps
// the pixel-grid mode its own units per em give it; the source's is basePixel.
func (s *buildState) prepareFamily(basePixel bool) error {
	pixel := make([]bool, len(s.options.Family))
	var jobs []func(context.Context) (*inventory.Face, error)
	for i, source := range s.options.Family {
		jobs = append(jobs, func(ctx context.Context) (*inventory.Face, error) {
			raw, err := os.ReadFile(source.Path)
			if err != nil {
				return nil, err
			}
			parsed, err := sfntio.Parse(raw, source.FaceIndex)
			if err != nil {
				return nil, err
			}
			if head := parsed.Tables["head"]; len(head) >= 20 {
				pixel[i] = binary.BigEndian.Uint16(head[18:]) < 1000
			}
			data, err := s.ops.Prepare(ctx, raw, uint32(source.FaceIndex), map[string]float32{}, s.options.ProcessOutlines)
			if err != nil {
				return nil, fmt.Errorf("family face %s#%d: %w", source.Path, source.FaceIndex, err)
			}
			return inventory.InspectFace(data)
		})
	}
	faces, err := runJobs(s.ctx, jobs, s.options.Workers, s.stage("Preparing family faces"))
	if err != nil {
		return err
	}
	s.family = append([]*inventory.Face{s.base}, faces...)
	s.familyPixel = map[string]bool{s.base.Digest: basePixel}
	for i, face := range faces {
		s.familyPixel[face.Digest] = pixel[i]
	}
	for _, face := range s.family {
		s.report.FamilyFaces = append(s.report.FamilyFaces, fmt.Sprintf("%s (%d)", face.Name, face.Weight))
	}
	return nil
}

// pickFace returns the family face a game weight uses and the weight to pass to
// synthetic emphasis, or 0 when the face is used as drawn.
func (s *buildState) pickFace(weight int) (*inventory.Face, int) {
	weights := make([]uint16, len(s.family))
	for i, face := range s.family {
		weights[i] = face.Weight
	}
	index, emphasis := inventory.PickFace(weights, s.options.WeightFaces, weight)
	return s.family[index], emphasis
}

// familyFallbacks lists the family faces other than used, nearest weight first.
func (s *buildState) familyFallbacks(used *inventory.Face, weight int) []*inventory.Face {
	var out []*inventory.Face
	for _, face := range s.family {
		if face.Digest != used.Digest {
			out = append(out, face)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return distance(int(out[i].Weight), weight) < distance(int(out[j].Weight), weight)
	})
	return out
}
