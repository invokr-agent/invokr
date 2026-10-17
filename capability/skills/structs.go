package skills

import "context"

type Manager interface {
	Specs(ctx context.Context) ([]Spec, error)
	Load(ctx context.Context, name string) (Skill, error)
}

type Spec struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type Skill struct {
	Spec
	Instructions string `json:"instructions"`
	Path         string `json:"path,omitempty"`
}
