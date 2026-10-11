package config

import (
	"strings"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

func confmapProvider(m map[string]interface{}) koanf.Provider {
	return confmap.Provider(m, ".")
}

func fileProvider(path string) koanf.Provider {
	return file.Provider(path)
}

func yamlParser() koanf.Parser {
	return yaml.Parser()
}

// envProvider reads the environment as koanf keys. Keys in listKeys take a
// comma-delimited value and become a []string, which is the one shape a
// list-typed field can unmarshal from: koanf's env provider stores whatever
// the callback returns, so a repeated option is the callback returning a slice
// (see env.ProviderWithValue). Every other key stays a string.
//
// A list key's VALUE that must contain a comma belongs in the config file or a
// repeated flag, where it needs no separator at all.
func envProvider(prefix string, listKeys map[string]bool) koanf.Provider {
	return env.ProviderWithValue(prefix, ".", func(s string, value string) (string, interface{}) {
		key := strings.ToLower(strings.TrimPrefix(s, prefix))
		if listKeys[key] {
			return key, SplitListValue(value)
		}
		return key, value
	})
}
