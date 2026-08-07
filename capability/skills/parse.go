package skills

import (
	"fmt"
	"os"
	"strings"

	"github.com/invokr-ai/invokr/support/frontmatter"
)

type frontMatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// ParseSkillFile reads and validates a SKILL.md file. fallbackName is used
// when the frontmatter omits a name (conventionally the directory name).
func ParseSkillFile(path string, fallbackName string) (Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, err
	}
	meta, body, err := frontmatter.Parse[frontMatter](data)
	if err != nil {
		return Skill{}, err
	}
	name := strings.TrimSpace(meta.Name)
	if name == "" {
		name = strings.TrimSpace(fallbackName)
	}
	description := strings.TrimSpace(meta.Description)
	instructions := strings.TrimSpace(body)
	if name == "" {
		return Skill{}, fmt.Errorf("name is required")
	}
	if description == "" {
		return Skill{}, fmt.Errorf("description is required")
	}
	if instructions == "" {
		return Skill{}, fmt.Errorf("instructions are required")
	}
	return Skill{Spec: Spec{Name: name, Description: description}, Instructions: instructions, Path: path}, nil
}
