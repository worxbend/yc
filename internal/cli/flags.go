package cli

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// optionalTextFlag is a string flag that remembers whether it was supplied.
//
// It exists because "not supplied" and "supplied as empty" mean different
// things at this layer: an absent flag must leave the environment or config
// value in place, and an empty one is a mistake worth rejecting rather than a
// way to clear a setting.
type optionalTextFlag struct {
	value string
	set   bool
}

// Set records a non-empty value.
func (f *optionalTextFlag) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("value cannot be empty")
	}
	f.value = value
	f.set = true
	return nil
}

// String renders the current value.
func (f *optionalTextFlag) String() string {
	if f == nil {
		return ""
	}
	return f.value
}

// optionalBoolFlag is a tri-state boolean: absent, true, or explicitly false.
type optionalBoolFlag struct {
	value bool
	set   bool
}

// Set parses a boolean value.
func (f *optionalBoolFlag) Set(value string) error {
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return err
	}
	f.value = parsed
	f.set = true
	return nil
}

// String renders the current value, or nothing when unset.
func (f *optionalBoolFlag) String() string {
	if f == nil || !f.set {
		return ""
	}
	return strconv.FormatBool(f.value)
}

// IsBoolFlag lets the flag package accept the bare spelling.
func (f *optionalBoolFlag) IsBoolFlag() bool { return true }

// enumFlag is a string flag constrained to a fixed set, validated at parse time
// so an unknown mode is a usage error naming the alternatives rather than a
// silent fallback discovered three screens later.
type enumFlag struct {
	name    string
	allowed []string
	value   string
	set     bool
}

// newEnumFlag returns an enum flag for the named setting.
func newEnumFlag(name string, allowed []string) enumFlag {
	return enumFlag{name: name, allowed: allowed}
}

// Set validates and records the value.
func (f *enumFlag) Set(value string) error {
	value = strings.ToLower(strings.TrimSpace(value))
	if !stringIn(value, f.allowed) {
		return fmt.Errorf("%s must be one of: %s", f.name, strings.Join(f.allowed, ", "))
	}
	f.value = value
	f.set = true
	return nil
}

// String renders the current value.
func (f *enumFlag) String() string {
	if f == nil {
		return ""
	}
	return f.value
}

// stringIn reports membership in a small fixed set.
func stringIn(value string, allowed []string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

// hasHelpArg reports whether the caller asked for help anywhere in a
// subcommand's arguments, so `yc login --help` prints the long-form
// explanation to stdout and exits zero instead of erroring out.
func hasHelpArg(args []string) bool {
	for _, arg := range args {
		switch arg {
		case "-h", "--help", "help":
			return true
		}
	}
	return false
}

// setIfNonEmpty assigns only when value carries something.
func setIfNonEmpty(dst *string, value string) {
	if strings.TrimSpace(value) != "" {
		*dst = value
	}
}
