package config

import (
	"reflect"
	"strconv"
	"strings"

	"github.com/worxbend/yc/internal/theme"
)

// valueKind is the small set of scalar shapes the flat config format supports.
// There are no nested tables and no arrays of tables, so four kinds cover every
// key; anything else in the struct is a programming error and is skipped rather
// than silently mis-parsed.
type valueKind int

const (
	kindString valueKind = iota
	kindBool
	kindInt
	kindStringList
)

// binding is one config key bound to the live field it writes.
//
// The bindings are derived from the struct tags rather than from a second
// hand-written table, because two lists of key names drift: twi's config grew a
// key that the parser knew and the redacted display did not, and the value only
// became visible when someone pasted `config show` output into an issue. Here
// the parser, the environment mapping, the redacted display, and the config
// writer all walk the same list.
type binding struct {
	// TOMLKey is the flat file key.
	TOMLKey string
	// EnvKeys are the environment variables that set this field, highest
	// precedence first. The YC_-prefixed name is always listed before any
	// unprefixed alias.
	EnvKeys []string
	// Secret marks a value that must never be displayed or written back to
	// the config file.
	Secret bool
	Kind   valueKind

	target reflect.Value
}

// paletteType is compared by type rather than by tag because internal/theme
// owns Palette and carries no config tags: colors are a theming concern, and
// the config key names for them belong here.
var paletteType = reflect.TypeOf(theme.Palette{})

// bindingsFor returns every config key bound to cfg's fields, in declaration
// order. The returned bindings alias cfg, so applying one mutates cfg directly
// and the list stays valid across mutations.
func bindingsFor(cfg *Config) []binding {
	if cfg == nil {
		return nil
	}
	var bindings []binding
	collectBindings(reflect.ValueOf(cfg).Elem(), &bindings)
	return bindings
}

// bindingIndex returns the bindings keyed by flat-file key.
func bindingIndex(cfg *Config) map[string]binding {
	bindings := bindingsFor(cfg)
	index := make(map[string]binding, len(bindings))
	for _, b := range bindings {
		index[b.TOMLKey] = b
	}
	return index
}

// collectBindings walks an addressable struct value, recursing through fields
// tagged `toml:",inline"` because the config file is flat: the Go type is
// grouped for readability, the file format is not.
func collectBindings(value reflect.Value, out *[]binding) {
	structType := value.Type()
	for i := 0; i < structType.NumField(); i++ {
		field := structType.Field(i)
		if !field.IsExported() {
			continue
		}
		tomlTag := strings.TrimSpace(field.Tag.Get("toml"))
		if tomlTag == "-" {
			continue
		}
		fieldValue := value.Field(i)
		if strings.HasPrefix(tomlTag, ",") && strings.Contains(tomlTag, "inline") {
			if fieldValue.Kind() != reflect.Struct {
				continue
			}
			if fieldValue.Type() == paletteType {
				collectPaletteBindings(fieldValue, out)
				continue
			}
			collectBindings(fieldValue, out)
			continue
		}
		key, _, _ := strings.Cut(tomlTag, ",")
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		kind, ok := kindOf(fieldValue)
		if !ok {
			continue
		}
		*out = append(*out, binding{
			TOMLKey: key,
			EnvKeys: splitEnvKeys(field.Tag.Get("env")),
			Secret:  strings.EqualFold(strings.TrimSpace(field.Tag.Get("secret")), "true"),
			Kind:    kind,
			target:  fieldValue,
		})
	}
}

// collectPaletteBindings maps the nine palette roles onto theme_<role> keys and
// YC_THEME_<ROLE> variables. The names are derived from the field names rather
// than listed, so a role added to theme.Palette gets a config key for free.
func collectPaletteBindings(value reflect.Value, out *[]binding) {
	paletteStruct := value.Type()
	for i := 0; i < paletteStruct.NumField(); i++ {
		field := paletteStruct.Field(i)
		if !field.IsExported() || field.Type.Kind() != reflect.String {
			continue
		}
		role := strings.ToLower(field.Name)
		*out = append(*out, binding{
			TOMLKey: "theme_" + role,
			EnvKeys: []string{"YC_THEME_" + strings.ToUpper(role)},
			Kind:    kindString,
			target:  value.Field(i),
		})
	}
}

// kindOf classifies a bound field, reporting false for a shape the flat format
// cannot express.
func kindOf(value reflect.Value) (valueKind, bool) {
	switch value.Kind() {
	case reflect.String:
		return kindString, true
	case reflect.Bool:
		return kindBool, true
	case reflect.Int:
		return kindInt, true
	case reflect.Slice:
		if value.Type().Elem().Kind() == reflect.String {
			return kindStringList, true
		}
	}
	return 0, false
}

// splitEnvKeys parses an env tag into its ordered alias list. A tag of "-" or
// an inline marker yields no keys, so the field is file-only.
func splitEnvKeys(tag string) []string {
	tag = strings.TrimSpace(tag)
	if tag == "" || tag == "-" || strings.HasPrefix(tag, ",") {
		return nil
	}
	parts := strings.Split(tag, ",")
	keys := make([]string, 0, len(parts))
	for _, part := range parts {
		if key := strings.TrimSpace(part); key != "" {
			keys = append(keys, key)
		}
	}
	return keys
}

// apply writes a raw string into the bound field.
//
// A malformed number or boolean keeps the value already there rather than
// failing the load: a typo in one display preference must not stop chat from
// starting, and the effective value stays visible through `yc config show`.
func (b binding) apply(raw string) {
	raw = strings.TrimSpace(raw)
	switch b.Kind {
	case kindString:
		b.target.SetString(raw)
	case kindBool:
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return
		}
		b.target.SetBool(parsed)
	case kindInt:
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return
		}
		b.target.SetInt(int64(parsed))
	case kindStringList:
		b.target.Set(reflect.ValueOf(splitList(raw)))
	}
}

// display renders the bound value for `yc config show`, replacing a non-empty
// secret with the placeholder. An empty secret stays empty, so an operator can
// tell "not configured" from "configured but hidden".
func (b binding) display() string {
	if b.Secret {
		if strings.TrimSpace(b.target.String()) == "" {
			return `""`
		}
		return strconv.Quote(Redacted)
	}
	return b.literal()
}

// literal renders the bound value as it would appear in the config file.
func (b binding) literal() string {
	switch b.Kind {
	case kindString:
		return strconv.Quote(RedactDisplayValue(b.target.String()))
	case kindBool:
		return strconv.FormatBool(b.target.Bool())
	case kindInt:
		return strconv.FormatInt(b.target.Int(), 10)
	case kindStringList:
		// Each element goes through the display redactor for the same reason
		// a scalar string does: default_chats holds user-supplied targets, and
		// a watch URL is a place an API key can be pasted. Redacting per
		// element rather than after joining keeps one poisoned entry from
		// blanking the whole list.
		values := make([]string, 0, b.target.Len())
		for i := 0; i < b.target.Len(); i++ {
			values = append(values, RedactDisplayValue(b.target.Index(i).String()))
		}
		return strconv.Quote(strings.Join(values, ","))
	}
	return `""`
}
