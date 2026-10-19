package pipeline

import (
	"context"
	"fmt"

	"hd2font/native/internal/atlas"
	"hd2font/native/internal/fontcore"
	"hd2font/native/internal/inventory"
)

// Build borrows processor; its owner is responsible for Close. Only invocation-
// local work is reused. The output is published after patch and ZIP readback.
func Build(ctx context.Context, inv *inventory.Inventory, processor *fontcore.Processor, options Options, progress func(string)) (*Report, error) {
	if processor == nil {
		return nil, fmt.Errorf("native font processor is required")
	}
	ops := &nativeOps{Processor: processor}
	return build(ctx, inv, ops, options, progress, atlas.Generate)
}

type nativeOps struct {
	*fontcore.Processor
}

func (n *nativeOps) Register(data, template []byte) ([]byte, error) {
	return fontcore.Register(data, template)
}
func (n *nativeOps) Axes(data []byte, index int) ([]axis, error) {
	values, err := fontcore.VariableAxes(data, index)
	if err != nil {
		return nil, err
	}
	result := make([]axis, len(values))
	for i, a := range values {
		result[i] = axis{a.Tag, a.Min, a.Default, a.Max}
	}
	return result, nil
}
