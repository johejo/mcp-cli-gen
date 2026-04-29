package mcpcli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/spf13/pflag"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		s    *jsonschema.Schema
		want flagKind
	}{
		{"string", &jsonschema.Schema{Type: "string"}, kindString},
		{"int", &jsonschema.Schema{Type: "integer"}, kindInt},
		{"number", &jsonschema.Schema{Type: "number"}, kindFloat},
		{"bool", &jsonschema.Schema{Type: "boolean"}, kindBool},
		{"object", &jsonschema.Schema{Type: "object"}, kindJSON},
		{"string array", &jsonschema.Schema{Type: "array", Items: &jsonschema.Schema{Type: "string"}}, kindStringSlice},
		{"int array", &jsonschema.Schema{Type: "array", Items: &jsonschema.Schema{Type: "integer"}}, kindIntSlice},
		{"object array", &jsonschema.Schema{Type: "array", Items: &jsonschema.Schema{Type: "object"}}, kindJSON},
		{"items missing", &jsonschema.Schema{Type: "array"}, kindJSON},
		{"oneOf", &jsonschema.Schema{OneOf: []*jsonschema.Schema{{Type: "string"}, {Type: "integer"}}}, kindJSON},
		{"anyOf", &jsonschema.Schema{AnyOf: []*jsonschema.Schema{{Type: "string"}}}, kindJSON},
		{"ref", &jsonschema.Schema{Ref: "#/$defs/X"}, kindJSON},
		{"empty type", &jsonschema.Schema{}, kindJSON},
		{"enum string", &jsonschema.Schema{Type: "string", Enum: []any{"a", "b"}}, kindString},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classify(tc.s)
			if got != tc.want {
				t.Errorf("classify(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// TestBindFlags_ReservedNameCollision exercises the regression for B1: a
// schema property literally named "parameters" (or "help") must not panic
// pflag's duplicate-flag detector.
func TestBindFlags_ReservedNameCollision(t *testing.T) {
	schema := &jsonschema.Schema{
		Properties: map[string]*jsonschema.Schema{
			"parameters": {Type: "string"},
			"help":       {Type: "string"},
			"message":    {Type: "string"},
		},
	}
	fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
	// Pre-register reserved flags the way buildToolCmd / cobra do.
	fs.String(flagParameters, "", "")

	var stderr bytes.Buffer
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("bindFlags panicked on collision: %v", r)
		}
	}()
	b := bindFlags(fs, schema, &stderr)

	// "parameters" must have been renamed away from --parameters.
	if got := b.flags["parameters"]; got == flagParameters {
		t.Errorf("property 'parameters' was not renamed; flag = %q", got)
	}
	if !strings.Contains(stderr.String(), "parameters") {
		t.Errorf("expected stderr warning about parameters, got: %q", stderr.String())
	}
	// "message" should keep its name.
	if got := b.flags["message"]; got != "message" {
		t.Errorf("property 'message' renamed unnecessarily: %q", got)
	}
	// All renamed flags must actually exist on fs.
	for _, fname := range b.flags {
		if fs.Lookup(fname) == nil {
			t.Errorf("flag --%s missing from FlagSet", fname)
		}
	}
}
