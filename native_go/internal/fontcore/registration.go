package fontcore

import "hd2font/native/internal/fontmeta"

type Axis = fontmeta.Axis

func VariableAxes(data []byte, index int) ([]Axis, error)           { return fontmeta.Axes(data, index) }
func Register(data, template []byte) ([]byte, error)                { return fontmeta.Register(data, template) }
func (p *Processor) Register(data, template []byte) ([]byte, error) { return Register(data, template) }
