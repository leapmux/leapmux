package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnvProvider(t *testing.T) {
	const prefix = "LEAPMUX_ENV_PROVIDER_TEST_"
	tests := []struct {
		name        string
		environment map[string]string
		listKeys    map[string]bool
		want        map[string]any
	}{
		{
			name: "filters the prefix and normalizes nested keys",
			environment: map[string]string{
				prefix + "DATA_DIR":                  "/data",
				prefix + "STORAGE.HOST":              "localhost",
				"LEAPMUX_ENV_PROVIDER_TESTING_VALUE": "ignored",
				"leapmux_env_provider_test_value":    "ignored",
			},
			want: map[string]any{
				"data_dir": "/data",
				"storage":  map[string]any{"host": "localhost"},
			},
		},
		{
			name: "preserves scalar values with no list keys",
			environment: map[string]string{
				prefix + "EMPTY":    "",
				prefix + "ZERO":     "0",
				prefix + "FALSE":    "false",
				prefix + "NEGATIVE": "-1",
				prefix + "TOKEN":    "a=b==",
				prefix + "VALUE":    " one, two ",
			},
			want: map[string]any{
				"empty": "", "zero": "0", "false": "false", "negative": "-1",
				"token": "a=b==", "value": " one, two ",
			},
		},
		{
			name: "splits only configured lists and preserves empty entries",
			environment: map[string]string{
				prefix + "LISTEN": " :8080, , unix:/a b.sock, ",
				prefix + "VALUE":  "one,two",
			},
			listKeys: map[string]bool{"listen": true},
			want: map[string]any{
				"listen": []string{":8080", "", "unix:/a b.sock", ""},
				"value":  "one,two",
			},
		},
		{
			name:        "preserves an explicitly empty list",
			environment: map[string]string{prefix + "LISTEN": ""},
			listKeys:    map[string]bool{"listen": true},
			want:        map[string]any{"listen": []string(nil)},
		},
		{
			name: "returns an empty map without matching variables",
			want: map[string]any{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for key, value := range tt.environment {
				t.Setenv(key, value)
			}
			got, err := envProvider(prefix, tt.listKeys).Read()
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
