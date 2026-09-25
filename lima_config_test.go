package lja

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

const (
	limaCreationFixtureDirectory = "testdata/lima-creation"
	limaCPUsKey                  = "cpus"
	nullValueError               = "must not be null"
	duplicateLimaKeyError        = "duplicate lima key"
	defaultTemplateTestName      = "default template"
	limaMemoryFixtureValue       = "8GiB"
	fedoraInputFixture           = "fedora/input.yaml"
	fedoraExpectedFixture        = "fedora/expected.yaml"
	relativeInputFixture         = "relative-reference/input.yaml"
	relativeExpectedFixture      = "relative-reference/expected.yaml"
)

func readLimaCreationFixture(t *testing.T, name string) []byte {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(limaCreationFixtureDirectory, name))
	assert.NilError(t, err)
	return content
}

func TestLimaConfiguration(t *testing.T) {
	for _, test := range []struct{ name, input, want string }{
		{emptyTestName, "lima: {}", ""},
		{"resources", "lima: {cpus: 4, memory: 8GiB, nestedVirtualization: true}", ""},
		{jsonNullValue, "lima: null", nullValueError},
		{limaListArguments, "lima: []", "must be a mapping"},
		{"duplicate", "lima: {cpus: 2, cpus: 4}", duplicateLimaKeyError},
		{"nested duplicate", "lima: {ssh: {localPort: 1, localPort: 2}}", duplicateLimaKeyError},
		{"nonstring key", "lima: {1: value}", "keys must be strings"},
		{limaMountsKey, "lima: {mounts: []}", "managed by LJA"},
		{"nested null", "lima: {ssh: {localPort: null}}", "lima values must be"},
		{"nonfinite", "lima: {cpus: .inf}", "invalid lima value"},
		{"alias", "lima: {cpus: &cpu 4, other: *cpu}", "aliases"},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, project := range []bool{false, true} {
				err := ValidateDevelopmentConfig([]byte(test.input), project)
				if test.want == "" {
					assert.NilError(t, err)
				} else {
					assert.ErrorContains(t, err, test.want)
					assert.ErrorContains(t, err, "line 1")
				}
			}
		})
	}
}

func TestLimaConfigurationMerging(t *testing.T) {
	global, err := decodeDevelopmentConfig([]byte("lima: {cpus: 2, memory: 4GiB, ssh: {localPort: 60022, loadDotSSHPubKeys: true}, dns: [1.1.1.1]}"), false)
	assert.NilError(t, err)
	project, err := decodeDevelopmentConfig([]byte("lima: {cpus: 4, ssh: {loadDotSSHPubKeys: false}, dns: []}"), true)
	assert.NilError(t, err)
	base := applyDevelopmentConfig(DefaultDevelopmentConfig(), global, "global")
	merged := applyDevelopmentConfig(base.clone(), project, "project")
	assert.DeepEqual(t, merged.Lima, map[string]any{
		limaCPUsKey: 4, limaMemoryKey: "4GiB", "ssh": map[string]any{"localPort": 60022, "loadDotSSHPubKeys": false}, "dns": []any{},
	})
	assert.Equal(t, base.Lima[limaCPUsKey], 2)
	ssh, ok := base.Lima["ssh"].(map[string]any)
	assert.Assert(t, ok)
	assert.Equal(t, ssh["loadDotSSHPubKeys"], true)
	encoded, err := yamlConfiguration(merged)
	assert.NilError(t, err)
	again, err := yamlConfiguration(merged.clone())
	assert.NilError(t, err)
	assert.Equal(t, string(encoded), string(again))
	assert.Assert(t, strings.HasPrefix(string(encoded), "gpg_forwarding: false\ngithub:\n  enabled: false\n  token_command: []\nlima:\n  cpus: 4\n  dns: []\n  memory: 4GiB\n"))
}

func TestLimaCreationInput(t *testing.T) {
	const literal = "$(touch nope); \"quoted\"\nnext"
	for _, test := range []struct {
		name   string
		config map[string]any
		want   string
	}{
		{name: defaultTemplateTestName, config: map[string]any{}, want: "base: template:default\n"},
		{name: "native configuration", config: map[string]any{
			limaCPUsKey:   4,
			limaMemoryKey: limaMemoryFixtureValue,
			"param":       map[string]any{"value": literal},
		}, want: "base: template:default\ncpus: 4\nmemory: 8GiB\nparam:\n  value: |-\n    $(touch nope); \"quoted\"\n    next\n"},
		{name: "explicit base", config: map[string]any{"base": ""}, want: "base: \"\"\n"},
		{name: "explicit images", config: map[string]any{"images": []any{}}, want: "images: []\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			original := mergeLimaConfig(nil, test.config)
			encoded, err := limaCreationInput(test.config)
			assert.NilError(t, err)
			assert.Equal(t, string(encoded), test.want)
			assert.DeepEqual(t, test.config, original)
		})
	}
	_, err := limaCreationInput(map[string]any{limaMountsKey: []any{}})
	assert.ErrorContains(t, err, "managed by LJA")
}

func TestLimaCreationInputPreservesGoldenFixtures(t *testing.T) {
	for _, test := range []struct {
		name     string
		global   string
		project  string
		expected string
	}{
		{name: defaultTemplateTestName, project: "default/input.yaml", expected: "default/expected.yaml"},
		{name: "Fedora template", project: fedoraInputFixture, expected: fedoraExpectedFixture},
		{name: "empty base", project: "empty-base/input.yaml", expected: "empty-base/expected.yaml"},
		{name: "empty images", project: "empty-images/input.yaml", expected: "empty-images/expected.yaml"},
		{name: "custom images", project: "custom-images/input.yaml", expected: "custom-images/expected.yaml"},
		{name: "relative reference", project: relativeInputFixture, expected: relativeExpectedFixture},
		{name: "inherited configuration", global: "inherited/global.yaml", project: "inherited/project.yaml", expected: "inherited/expected.yaml"},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := DefaultDevelopmentConfig()
			if test.global != "" {
				global, err := decodeDevelopmentConfig(readLimaCreationFixture(t, test.global), false)
				assert.NilError(t, err)
				config = applyDevelopmentConfig(config, global, "global")
			}
			project, err := decodeDevelopmentConfig(readLimaCreationFixture(t, test.project), true)
			assert.NilError(t, err)
			config = applyDevelopmentConfig(config, project, "project")

			encoded, err := limaCreationInput(config.Lima)
			assert.NilError(t, err)
			assert.Equal(t, string(encoded), string(readLimaCreationFixture(t, test.expected)))
		})
	}
}
