package mcpcli

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/spf13/pflag"
)

// reservedFlagNames are flag names we own ourselves or that cobra/pflag reserve.
// Properties whose name collides with these are renamed to avoid panics or
// conflicting semantics. Keep this list in sync with flags actually declared
// on tool subcommands (see buildToolCmd) and cobra defaults (--help).
var reservedFlagNames = map[string]bool{
	flagParameters: true,
	"help":         true,
}

const flagParameters = "parameters"

type flagKind int

const (
	kindString flagKind = iota
	kindInt
	kindFloat
	kindBool
	kindStringSlice
	kindIntSlice
	kindFloatSlice
	kindBoolSlice
	kindJSON // free-form: --name '<json>'; unmarshalled before sending
)

// boundFlags remembers how each property was wired so the values can be
// collected back from a *pflag.FlagSet after parse.
type boundFlags struct {
	schema *jsonschema.Schema
	props  map[string]flagKind // property name -> kind
	flags  map[string]string   // property name -> flag name (currently identity, future-proof)
	fs     *pflag.FlagSet
}

// bindFlags declares one flag per top-level property of schema on fs.
// schema must be the tool inputSchema (type: object). Property names that
// collide with reservedFlagNames or with each other after sanitization are
// renamed (with a stderr warning) to avoid pflag's duplicate-name panic.
func bindFlags(fs *pflag.FlagSet, schema *jsonschema.Schema, stderr io.Writer) *boundFlags {
	b := &boundFlags{
		schema: schema,
		props:  map[string]flagKind{},
		flags:  map[string]string{},
		fs:     fs,
	}
	if schema == nil {
		return b
	}
	used := map[string]bool{}
	for n := range reservedFlagNames {
		used[n] = true
	}
	// Iterate properties in deterministic order so renames are stable.
	names := make([]string, 0, len(schema.Properties))
	for name := range schema.Properties {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		p := schema.Properties[name]
		kind := classify(p)
		flagName := uniqueFlagName(name, used)
		if flagName != name && stderr != nil {
			fmt.Fprintf(stderr, "warning: property %q exposed as --%s (name collision)\n", name, flagName)
		}
		used[flagName] = true
		b.props[name] = kind
		b.flags[name] = flagName
		usage := flagUsage(p, kind)
		switch kind {
		case kindString, kindJSON:
			fs.String(flagName, "", usage)
		case kindInt:
			fs.Int64(flagName, 0, usage)
		case kindFloat:
			fs.Float64(flagName, 0, usage)
		case kindBool:
			fs.Bool(flagName, false, usage)
		case kindStringSlice:
			fs.StringSlice(flagName, nil, usage)
		case kindIntSlice:
			fs.Int64Slice(flagName, nil, usage)
		case kindFloatSlice:
			fs.Float64Slice(flagName, nil, usage)
		case kindBoolSlice:
			fs.BoolSlice(flagName, nil, usage)
		}
	}
	return b
}

// uniqueFlagName returns a flag name not present in used; suffixes "_" until
// no collision remains.
func uniqueFlagName(candidate string, used map[string]bool) string {
	name := candidate
	for used[name] {
		name += "_"
	}
	return name
}

// classify picks the cobra flag type for a property schema.
func classify(p *jsonschema.Schema) flagKind {
	if p == nil {
		return kindJSON
	}
	if len(p.OneOf) > 0 || len(p.AnyOf) > 0 || p.Ref != "" || len(p.AllOf) > 0 {
		return kindJSON
	}
	t := p.Type
	if t == "" && len(p.Types) == 1 {
		t = p.Types[0]
	}
	switch t {
	case "string":
		return kindString
	case "integer":
		return kindInt
	case "number":
		return kindFloat
	case "boolean":
		return kindBool
	case "object":
		return kindJSON
	case "array":
		if p.Items == nil {
			return kindJSON
		}
		it := p.Items.Type
		if it == "" && len(p.Items.Types) == 1 {
			it = p.Items.Types[0]
		}
		switch it {
		case "string":
			return kindStringSlice
		case "integer":
			return kindIntSlice
		case "number":
			return kindFloatSlice
		case "boolean":
			return kindBoolSlice
		default:
			return kindJSON
		}
	default:
		return kindJSON
	}
}

func flagUsage(p *jsonschema.Schema, kind flagKind) string {
	if p == nil {
		return ""
	}
	parts := []string{}
	if p.Description != "" {
		parts = append(parts, p.Description)
	}
	if len(p.Enum) > 0 {
		labels := make([]string, 0, len(p.Enum))
		for _, e := range p.Enum {
			labels = append(labels, fmt.Sprintf("%v", e))
		}
		parts = append(parts, fmt.Sprintf("(one of: %s)", strings.Join(labels, ", ")))
	}
	if kind == kindJSON {
		parts = append(parts, "(JSON)")
	}
	return strings.Join(parts, " ")
}

// collect reads back every property flag that was set and returns the
// resulting argument map. JSON-kind flags are unmarshalled before insertion.
func (b *boundFlags) collect() (map[string]any, error) {
	out := map[string]any{}
	for name, kind := range b.props {
		flagName := b.flags[name]
		f := b.fs.Lookup(flagName)
		if f == nil || !f.Changed {
			continue
		}
		v, err := readFlag(b.fs, flagName, kind)
		if err != nil {
			return nil, fmt.Errorf("flag --%s: %w", flagName, err)
		}
		out[name] = v
	}
	return out, nil
}

func readFlag(fs *pflag.FlagSet, name string, kind flagKind) (any, error) {
	switch kind {
	case kindString:
		return fs.GetString(name)
	case kindInt:
		return fs.GetInt64(name)
	case kindFloat:
		return fs.GetFloat64(name)
	case kindBool:
		return fs.GetBool(name)
	case kindStringSlice:
		return fs.GetStringSlice(name)
	case kindIntSlice:
		return fs.GetInt64Slice(name)
	case kindFloatSlice:
		return fs.GetFloat64Slice(name)
	case kindBoolSlice:
		return fs.GetBoolSlice(name)
	case kindJSON:
		raw, err := fs.GetString(name)
		if err != nil {
			return nil, err
		}
		var v any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, fmt.Errorf("invalid JSON: %w", err)
		}
		return v, nil
	}
	return nil, fmt.Errorf("unknown flag kind for %s", name)
}

// missingRequired returns the names of required properties that have no
// corresponding flag set on fs. Used only when --parameters is absent.
func (b *boundFlags) missingRequired() []string {
	if b.schema == nil {
		return nil
	}
	var missing []string
	for _, name := range b.schema.Required {
		if _, ok := b.props[name]; !ok {
			continue
		}
		flagName := b.flags[name]
		f := b.fs.Lookup(flagName)
		if f == nil || !f.Changed {
			missing = append(missing, name)
		}
	}
	return missing
}
