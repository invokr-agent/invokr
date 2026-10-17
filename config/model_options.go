package config

import (
	"fmt"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"

	inference "github.com/rivt-ai/go-inference-router"
)

// ModelOption is the shared validation, menu and request contract. Empty values
// inherit existing behavior; "omit" explicitly leaves the request field absent.
type ModelOption struct {
	Key, Label, Type, Env string
	Choices               []string
	Default               string // Empty means inherit the existing request, not a new wire default.
	Min, Max              float64
	LegacyEnv             bool
	Read                  func(inference.Request) string
	Apply                 func(*inference.Request, string)
}

var ModelOptionRegistry = []ModelOption{
	{Key: "max_tokens", Label: "Output tokens (includes reasoning)", Type: "integer", Env: "INVOKR_N_PREDICT", Min: 1, Max: math.MaxInt32, LegacyEnv: true,
		Read: func(r inference.Request) string {
			if r.MaxOutputTokens <= 0 {
				return "omit"
			}
			return strconv.Itoa(r.MaxOutputTokens)
		},
		Apply: func(r *inference.Request, value string) {
			n := optionNumericValue(value)
			r.MaxOutputTokens = 0
			if n != nil {
				r.MaxOutputTokens = int(*n)
			}
		}},
	{Key: "temperature", Label: "Temperature", Type: "number", Env: "INVOKR_TEMPERATURE", Min: 0, Max: 2, LegacyEnv: true,
		Read:  func(r inference.Request) string { return optionNumber(r.Temperature) },
		Apply: func(r *inference.Request, value string) { r.Temperature = optionNumericValue(value) }},
	{Key: "top_p", Label: "Top p", Type: "number", Env: "INVOKR_TOP_P", Min: 0, Max: 1,
		Read:  func(r inference.Request) string { return optionNumber(r.TopP) },
		Apply: func(r *inference.Request, value string) { r.TopP = optionNumericValue(value) }},
}

func optionNumber(n *float64) string {
	if n == nil {
		return "omit"
	}
	return strconv.FormatFloat(*n, 'g', -1, 64)
}

// optionNumericValue is used only after registry validation.
func optionNumericValue(value string) *float64 {
	if value == "omit" {
		return nil
	}
	n, _ := strconv.ParseFloat(value, 64)
	return &n
}

func (o ModelOption) Validate(value string) error {
	if value == "" || value == "omit" {
		return nil
	}
	if len(o.Choices) > 0 {
		if slices.Contains(o.Choices, value) {
			return nil
		}
		return fmt.Errorf("%s: expected one of %s", o.Label, strings.Join(o.Choices, ", "))
	}
	return o.validateNumber(value)
}

func (o ModelOption) validateNumber(value string) error {
	n, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fmt.Errorf("%s: expected %s or omit: %w", o.Label, o.Type, err)
	}
	if math.IsNaN(n) || math.IsInf(n, 0) || n < o.Min || n > o.Max || (o.Type == "integer" && math.Trunc(n) != n) {
		return fmt.Errorf("%s: expected %s between %g and %g, or omit", o.Label, o.Type, o.Min, o.Max)
	}
	return nil
}

// Override keeps existing environment/flag resolution authoritative. Legacy
// environment values are already represented by the gateway's base request.
func (o ModelOption) Override(c AgentConfig, model string) (value, source string) {
	if o.Key == "temperature" && c.TemperatureDisabled {
		return "omit", "agent.temperature_disabled (startup settings)"
	}
	if value = strings.TrimSpace(os.Getenv(o.Env)); value != "" {
		if o.LegacyEnv {
			return "", o.Env + " (startup settings, including flags)"
		}
		return value, o.Env
	}
	if value = c.ModelOptions[model][o.Key]; value != "" {
		return value, "model config"
	}
	if o.Default != "" {
		return o.Default, "registry default"
	}
	return "", "inherited startup settings"
}

func ValidateModelOptionValues(values map[string]string) error {
	for key, value := range values {
		i := slices.IndexFunc(ModelOptionRegistry, func(o ModelOption) bool { return o.Key == key })
		if i < 0 {
			return fmt.Errorf("unknown model option %q", key)
		}
		if err := ModelOptionRegistry[i].Validate(value); err != nil {
			return err
		}
	}
	return nil
}

func (c AgentConfig) ValidateModelOptions() error {
	for model, values := range c.ModelOptions {
		if err := ValidateModelOptionValues(values); err != nil {
			return fmt.Errorf("model %q: %w", model, err)
		}
	}
	for _, o := range ModelOptionRegistry {
		if o.LegacyEnv {
			continue
		}
		if err := o.Validate(strings.TrimSpace(os.Getenv(o.Env))); err != nil {
			return fmt.Errorf("%s: %w", o.Env, err)
		}
	}
	return nil
}

// SetModelOptions patches only this model, preserving other raw config and
// never persisting environment values or resolved defaults.
func SetModelOptions(path, model string, values map[string]string) error {
	if strings.TrimSpace(model) == "" {
		return fmt.Errorf("model options require a model")
	}
	if err := ValidateModelOptionValues(values); err != nil {
		return err
	}
	return editRawConfig(path, true, func(raw map[string]any) error {
		if raw == nil {
			return fmt.Errorf("model options require a JSON config object")
		}
		models := childMap(childMap(raw, "agent"), "model_options")
		next := map[string]string{}
		for key, value := range values {
			if value != "" {
				next[key] = value
			}
		}
		if len(next) == 0 {
			delete(models, model)
		} else {
			models[model] = next
		}
		return nil
	})
}

type ModelOptionValue struct {
	Option                      ModelOption
	Effective, Source, Override string
}
