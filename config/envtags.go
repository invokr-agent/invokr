package config

import (
	"reflect"
	"strconv"
	"strings"
	"time"
)

var (
	durationType, stringMapKey = reflect.TypeOf(time.Duration(0)), reflect.TypeOf(map[string]string(nil))
	intMapType                 = reflect.TypeOf(map[string]int(nil))
)

// applyEnv overwrites every field tagged `env:"NAME"` in the struct pointed to
// by ptr with the value of that environment variable, recursing into nested
// structs. Unset, blank, or unparsable variables leave the field untouched —
// the same contract the hand-written envX helpers had. A tag may name several
// variables ("A,B"); they are applied left to right, so the last one that is
// set wins.
//
// Conditional cases (tri-state *bool fields, the tokf enable/disable
// inversion, derived fields) are not expressible as tags and stay hand-written
// in the withEnv methods.
func applyEnv(ptr any) {
	walkTagged(reflect.ValueOf(ptr).Elem(), func(field reflect.Value, tag reflect.StructTag) {
		names, ok := tag.Lookup("env")
		if !ok {
			return
		}
		for _, name := range strings.Split(names, ",") {
			if value := env(name); value != "" {
				setFromString(field, value)
			}
		}
	})
}

func walkTagged(v reflect.Value, visit func(reflect.Value, reflect.StructTag)) {
	t := v.Type()
	for i := range t.NumField() {
		field, value := t.Field(i), v.Field(i)
		switch {
		case !field.IsExported():
		case value.Kind() == reflect.Struct:
			walkTagged(value, visit)
		default:
			visit(value, field.Tag)
		}
	}
}

// setFromString assigns raw to field, parsed according to the field's type.
// Parse failures and unsupported types leave the field unchanged.
func setFromString(field reflect.Value, raw string) {
	switch field.Type() {
	case durationType:
		if parsed, err := time.ParseDuration(raw); err == nil {
			field.SetInt(int64(parsed))
		}
	case intMapType:
		if parsed := parseIntMap(raw); len(parsed) > 0 {
			field.Set(reflect.ValueOf(parsed))
		}
	case stringMapKey:
		field.Set(reflect.ValueOf(mergeEnvMap(field.Interface().(map[string]string), parseEnvMap(raw))))
	default:
		setScalar(field, raw)
	}
}

// parseIntMap reads a "name=5,other=8" list; an entry whose value is not a
// number is dropped, like every other unparsable env value.
func parseIntMap(raw string) map[string]int {
	out := map[string]int{}
	for name, value := range parseEnvMap(raw) {
		if parsed, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
			out[name] = parsed
		}
	}
	return out
}

func setScalar(field reflect.Value, raw string) {
	switch field.Kind() {
	case reflect.String:
		field.SetString(raw)
	case reflect.Bool:
		if parsed, err := strconv.ParseBool(raw); err == nil {
			field.SetBool(parsed)
		}
	case reflect.Int, reflect.Int64:
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil {
			field.SetInt(parsed)
		}
	case reflect.Float64:
		if parsed, err := strconv.ParseFloat(raw, 64); err == nil {
			field.SetFloat(parsed)
		}
	case reflect.Slice:
		if field.Type().Elem().Kind() == reflect.String {
			field.Set(reflect.ValueOf(splitList(raw)))
		}
	}
}
