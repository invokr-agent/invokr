package core

func UpsertToolSpec(specs []ToolSpec, incoming ToolSpec) []ToolSpec {
	for i := range specs {
		if specs[i].Name == incoming.Name {
			specs[i] = incoming
			return specs
		}
	}
	return append(specs, incoming)
}
