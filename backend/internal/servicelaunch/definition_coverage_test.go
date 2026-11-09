package servicelaunch

import (
	"bytes"
	"maps"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/lifecycle"
)

// The renderer coverage tests make each native mechanism classify every
// field of LaunchSpec. A field is either carried by the native definition,
// so that a change to it is drift, or read from the saved specification at
// run time. A new field fails TestLaunchSpecFieldVariantsCoverEveryField
// until launchSpecFieldVariants has a mutator for it, and it fails every
// renderer test until each mechanism classifies it.

// exportedFieldNames lists the exported fields of the struct type T.
func exportedFieldNames[T any]() []string {
	typ := reflect.TypeFor[T]()
	var names []string
	for index := range typ.NumField() {
		if field := typ.Field(index); field.IsExported() {
			names = append(names, field.Name)
		}
	}
	return names
}

// launchSpecFieldVariants returns, for each field of LaunchSpec, a copy of
// base that differs from base in that field alone.
func launchSpecFieldVariants(base LaunchSpec) map[string]LaunchSpec {
	variant := func(mutate func(*LaunchSpec)) LaunchSpec {
		spec := base
		// The copy must not share the backing array or the map with base.
		spec.RuntimeArguments = slices.Clone(base.RuntimeArguments)
		spec.RuntimeEnvironment = maps.Clone(base.RuntimeEnvironment)
		mutate(&spec)
		return spec
	}
	otherMode := lifecycle.RoleHub
	if base.Mode == lifecycle.RoleHub {
		otherMode = lifecycle.RoleWorker
	}
	otherStartup := lifecycle.StartupBoot
	if base.Startup == lifecycle.StartupBoot {
		otherStartup = lifecycle.StartupLogin
	}
	environmentFile := base.DataDirectory + "-environment.yaml"
	if base.EnvironmentFile != nil {
		environmentFile = *base.EnvironmentFile + "-variant"
	}
	return map[string]LaunchSpec{
		"InstanceID":       variant(func(spec *LaunchSpec) { spec.InstanceID += "0" }),
		"Mode":             variant(func(spec *LaunchSpec) { spec.Mode = otherMode }),
		"Startup":          variant(func(spec *LaunchSpec) { spec.Startup = otherStartup }),
		"AccountID":        variant(func(spec *LaunchSpec) { spec.AccountID += "0" }),
		"AccountName":      variant(func(spec *LaunchSpec) { spec.AccountName += "-variant" }),
		"Executable":       variant(func(spec *LaunchSpec) { spec.Executable += "-variant" }),
		"WorkingDirectory": variant(func(spec *LaunchSpec) { spec.WorkingDirectory += "-variant" }),
		"ConfigFile":       variant(func(spec *LaunchSpec) { spec.ConfigFile += "-variant" }),
		"DataDirectory":    variant(func(spec *LaunchSpec) { spec.DataDirectory += "-variant" }),
		"RuntimeArguments": variant(func(spec *LaunchSpec) {
			spec.RuntimeArguments = append(spec.RuntimeArguments, "--log-level", "debug")
		}),
		"RuntimeEnvironment": variant(func(spec *LaunchSpec) {
			if spec.RuntimeEnvironment == nil {
				spec.RuntimeEnvironment = map[string]string{}
			}
			spec.RuntimeEnvironment["LEAPMUX_VARIANT"] = "variant"
		}),
		"EnvironmentFile": variant(func(spec *LaunchSpec) { spec.EnvironmentFile = &environmentFile }),
	}
}

func TestLaunchSpecFieldVariantsCoverEveryField(t *testing.T) {
	minimal := LaunchSpec(testSpec())
	complete := minimal
	complete.Mode = lifecycle.RoleHub
	complete.Startup = lifecycle.StartupBoot
	complete.RuntimeArguments = []string{"--name", "laptop"}
	complete.RuntimeEnvironment = map[string]string{"PATH": "/usr/bin"}
	environmentFile := filepath.Join(minimal.DataDirectory, "environment.yaml")
	complete.EnvironmentFile = &environmentFile
	fields := exportedFieldNames[LaunchSpec]()
	for name, base := range map[string]LaunchSpec{"minimal": minimal, "complete": complete} {
		t.Run(name, func(t *testing.T) {
			// A deep copy of base, so that a mutator that writes through a
			// slice, a map, or a pointer of base fails the last assertion.
			original := base
			original.RuntimeArguments = slices.Clone(base.RuntimeArguments)
			original.RuntimeEnvironment = maps.Clone(base.RuntimeEnvironment)
			if base.EnvironmentFile != nil {
				original.EnvironmentFile = new(*base.EnvironmentFile)
			}
			variants := launchSpecFieldVariants(base)
			require.ElementsMatch(t, fields, slices.Collect(maps.Keys(variants)), "every LaunchSpec field needs a mutator")
			for changed, variant := range variants {
				for _, field := range fields {
					before := reflect.ValueOf(base).FieldByName(field).Interface()
					after := reflect.ValueOf(variant).FieldByName(field).Interface()
					assert.Equal(t, field == changed, !reflect.DeepEqual(before, after), "the %s variant must change %s and no other field; checked %s", changed, changed, field)
				}
			}
			assert.Equal(t, original, base, "a variant must not change base")
		})
	}
}

// fieldsCarriedBy completes the classification of one mechanism. carried
// classifies the fields that depend on the mechanism. No native definition
// carries the fields below: the managed run reads each one from the saved
// specification at run time (managedRun and applyManagedEnvironment in
// cmd/leapmux/lifecycle_managed.go).
func fieldsCarriedBy(carried map[string]bool) map[string]bool {
	classification := map[string]bool{
		// managedRun passes this path with --config.
		"ConfigFile": false,
		// managedRun appends these arguments to the run command.
		"RuntimeArguments": false,
		// applyManagedEnvironment sets these variables.
		"RuntimeEnvironment": false,
		// applyManagedEnvironment loads the variables of this file.
		"EnvironmentFile": false,
	}
	maps.Copy(classification, carried)
	return classification
}

// assertDefinitionCarries checks one renderer against its classification of
// the LaunchSpec fields. render returns the identity of the definition, such
// as its path or its name, together with its content, so a field that
// changes either one counts as carried.
func assertDefinitionCarries(t *testing.T, base LaunchSpec, render func(LaunchSpec) []byte, carried map[string]bool) {
	t.Helper()
	require.ElementsMatch(t, exportedFieldNames[LaunchSpec](), slices.Collect(maps.Keys(carried)), "the mechanism must classify every LaunchSpec field")
	rendered := render(base)
	for field, variant := range launchSpecFieldVariants(base) {
		changed := !bytes.Equal(rendered, render(variant))
		if carried[field] {
			assert.True(t, changed, "the definition does not change with %s, which the mechanism classifies as carried", field)
		} else {
			assert.False(t, changed, "the definition changes with %s; classify the field as carried", field)
		}
	}
}
