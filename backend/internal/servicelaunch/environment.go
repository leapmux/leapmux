package servicelaunch

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"go.yaml.in/yaml/v3"
)

// LoadEnvironment reads the YAML map shared by every native launch path.
// Explicit saved overrides take precedence over the file. Neither source
// may replace the selected account's identity or home directory.
func (s LaunchSpec) LoadEnvironment() (map[string]string, error) {
	return s.loadEnvironment(runtime.GOOS == "windows")
}

func (s LaunchSpec) loadEnvironment(caseInsensitive bool) (map[string]string, error) {
	keyOf := func(name string) string {
		if caseInsensitive {
			return strings.ToUpper(name)
		}
		return name
	}
	values := make(map[string]string)
	if s.EnvironmentFile != nil {
		path := *s.EnvironmentFile
		if !strings.HasPrefix(path, "/") && !windowsAbsolute(path) {
			return nil, fmt.Errorf("servicelaunch: the environment file must have an absolute path")
		}
		file, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("servicelaunch: open the environment file %q: %w", path, err)
		}
		defer func() { _ = file.Close() }()
		info, err := file.Stat()
		if err != nil {
			return nil, fmt.Errorf("servicelaunch: inspect the environment file %q: %w", path, err)
		}
		const maxEnvironmentBytes = 16 << 20
		if !info.Mode().IsRegular() || info.Size() > maxEnvironmentBytes {
			return nil, fmt.Errorf("servicelaunch: the environment file %q must be a regular file of at most %d bytes", path, maxEnvironmentBytes)
		}
		decoder := yaml.NewDecoder(io.LimitReader(file, maxEnvironmentBytes+1))
		var document yaml.Node
		if err := decoder.Decode(&document); err != nil {
			// Parser errors can contain secret values from the input.
			return nil, fmt.Errorf("servicelaunch: the environment file %q must contain one YAML map of strings", path)
		}
		if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
			return nil, fmt.Errorf("servicelaunch: the environment file %q must contain a YAML map of strings", path)
		}
		mapping := document.Content[0]
		for i := 0; i < len(mapping.Content); i += 2 {
			key, value := mapping.Content[i], mapping.Content[i+1]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
				return nil, fmt.Errorf("servicelaunch: each environment entry in %q must have a string key and a string value", path)
			}
			name := keyOf(key.Value)
			if _, exists := values[name]; exists {
				return nil, fmt.Errorf("servicelaunch: the environment file %q contains a duplicate key", path)
			}
			if err := validateEnvironmentEntry(key.Value, value.Value); err != nil {
				return nil, fmt.Errorf("servicelaunch: environment file %q: %w", path, err)
			}
			values[name] = value.Value
		}
		var extra yaml.Node
		if err := decoder.Decode(&extra); err != io.EOF {
			return nil, fmt.Errorf("servicelaunch: the environment file %q must contain exactly one YAML document", path)
		}
	}
	runtimeKeys := make(map[string]bool)
	for key, value := range s.RuntimeEnvironment {
		name := keyOf(key)
		if runtimeKeys[name] {
			return nil, fmt.Errorf("the runtime environment contains duplicate variable names")
		}
		runtimeKeys[name] = true
		if err := validateEnvironmentEntry(key, value); err != nil {
			return nil, err
		}
		values[name] = value
	}
	return values, nil
}

func validateEnvironmentEntry(key, value string) error {
	if key == "" {
		return fmt.Errorf("the environment contains an empty key")
	}
	for i, character := range key {
		if character != '_' && (character < 'A' || character > 'Z') && (character < 'a' || character > 'z') && (i == 0 || character < '0' || character > '9') {
			return fmt.Errorf("the environment contains an invalid key")
		}
	}
	if protectedIdentityVariables[strings.ToUpper(key)] {
		return fmt.Errorf("%s states the service account's identity and cannot come from the runtime environment", key)
	}
	if strings.ContainsRune(value, 0) {
		return fmt.Errorf("the environment value for %s contains a null character", key)
	}
	return nil
}
