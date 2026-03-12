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

func envProvider(prefix string) koanf.Provider {
	return env.Provider(prefix, ".", func(s string) string {
		return strings.ToLower(strings.TrimPrefix(s, prefix))
	})
}
