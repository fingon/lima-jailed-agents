package lja

import (
	"encoding/json"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

func TestLimaConfiguration(t *testing.T) {
	for _, test := range []struct{ name, input, want string }{
		{"empty", "lima: {}", ""},
		{"resources", "lima: {cpus: 4, memory: 8GiB, nestedVirtualization: true}", ""},
		{"null", "lima: null", "must not be null"},
		{"list", "lima: []", "must be a mapping"},
		{"duplicate", "lima: {cpus: 2, cpus: 4}", "duplicate lima key"},
		{"nested duplicate", "lima: {ssh: {localPort: 1, localPort: 2}}", "duplicate lima key"},
		{"nonstring key", "lima: {1: value}", "keys must be strings"},
		{"mounts", "lima: {mounts: []}", "managed by LJA"},
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
		"cpus": 4, "memory": "4GiB", "ssh": map[string]any{"localPort": 60022, "loadDotSSHPubKeys": false}, "dns": []any{},
	})
	assert.Equal(t, base.Lima["cpus"], 2)
	assert.Equal(t, base.Lima["ssh"].(map[string]any)["loadDotSSHPubKeys"], true)
	encoded, err := yamlConfiguration(merged)
	assert.NilError(t, err)
	again, err := yamlConfiguration(merged.clone())
	assert.NilError(t, err)
	assert.Equal(t, string(encoded), string(again))
	assert.Assert(t, strings.HasPrefix(string(encoded), "gpg_forwarding: false\nlima:\n  cpus: 4\n  dns: []\n  memory: 4GiB\n"))
}

func TestLimaOverrideArguments(t *testing.T) {
	const literal = "$(touch nope); \"quoted\"\nnext"
	config := map[string]any{"cpus": 4, "memory": "8GiB", "param": map[string]any{"value": literal}}
	arguments, err := limaOverrideArguments(config)
	assert.NilError(t, err)
	assert.Equal(t, len(arguments), 6)
	assert.DeepEqual(t, arguments[:4], []string{"--set", ".[\"cpus\"] = 4", "--set", ".[\"memory\"] = \"8GiB\""})
	var value map[string]any
	assert.NilError(t, json.Unmarshal([]byte(strings.SplitN(arguments[5], " = ", 2)[1]), &value))
	assert.Equal(t, value["value"], literal)
	_, err = limaOverrideArguments(map[string]any{limaMountsKey: []any{}})
	assert.ErrorContains(t, err, "managed by LJA")
}
