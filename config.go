package lja

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	configPackages                      = "packages"
	configCopyGitConfig                 = "copy_git_config"
	configEnvironment                   = "env"
	configEnvironmentPassthrough        = "env_passthrough"
	configSetup                         = "setup"
	configInheritSetup                  = "inherit_setup"
	configInheritEnvironmentPassthrough = "inherit_env_passthrough"
	makeCommand                         = "make"
	packageInstalledStatus              = "install ok installed"
)

var (
	environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	packageNamePattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]*(?::[a-z0-9][a-z0-9-]*)?$`)
)

type SetupCommand struct {
	Source  string
	Index   int
	Command string
}

type DevelopmentConfig struct {
	Packages       []string
	CopyGitConfig  bool
	Env            map[string]string
	EnvPassthrough []string
	Setup          []SetupCommand
}

type developmentConfigJSON struct {
	Packages       []string          `json:"packages"`
	CopyGitConfig  bool              `json:"copy_git_config"`
	Env            map[string]string `json:"env"`
	EnvPassthrough []string          `json:"env_passthrough"`
	Setup          []string          `json:"setup"`
}

func DefaultDevelopmentConfig() DevelopmentConfig {
	return DevelopmentConfig{
		Packages:       []string{gitCommand, makeCommand},
		CopyGitConfig:  true,
		Env:            make(map[string]string),
		EnvPassthrough: []string{},
		Setup:          []SetupCommand{},
	}
}

func (config DevelopmentConfig) clone() DevelopmentConfig {
	cloned := DevelopmentConfig{
		Packages:       append([]string{}, config.Packages...),
		CopyGitConfig:  config.CopyGitConfig,
		Env:            make(map[string]string, len(config.Env)),
		EnvPassthrough: append([]string{}, config.EnvPassthrough...),
		Setup:          append([]SetupCommand{}, config.Setup...),
	}
	for name, value := range config.Env {
		cloned.Env[name] = value
	}
	return cloned
}

func (config DevelopmentConfig) AsJSON() developmentConfigJSON {
	setup := make([]string, 0, len(config.Setup))
	for _, command := range config.Setup {
		setup = append(setup, command.Command)
	}
	packages := append([]string{}, config.Packages...)
	passthrough := append([]string{}, config.EnvPassthrough...)
	environment := make(map[string]string, len(config.Env))
	for name, value := range config.Env {
		environment[name] = value
	}
	return developmentConfigJSON{
		Packages:       packages,
		CopyGitConfig:  config.CopyGitConfig,
		Env:            environment,
		EnvPassthrough: passthrough,
		Setup:          setup,
	}
}

func (config DevelopmentConfig) ResolveEnvironment(environment map[string]string) (map[string]string, error) {
	resolved := make(map[string]string, len(config.Env)+len(config.EnvPassthrough))
	for _, name := range config.EnvPassthrough {
		if _, explicit := config.Env[name]; explicit {
			continue
		}
		value, present := environment[name]
		if !present {
			return nil, ljaError("caller environment variable %s is not set", name)
		}
		resolved[name] = value
	}
	for name, value := range config.Env {
		if strings.IndexByte(value, 0) >= 0 {
			return nil, ljaError("environment variable %s contains a NUL character", name)
		}
		resolved[name] = value
	}
	for name, value := range resolved {
		if strings.IndexByte(value, 0) >= 0 {
			return nil, ljaError("environment variable %s contains a NUL character", name)
		}
	}
	return resolved, nil
}

type sourceDevelopmentConfig struct {
	Packages                         []string
	HasPackages                      bool
	CopyGitConfig                    bool
	HasCopyGitConfig                 bool
	Env                              map[string]string
	HasEnv                           bool
	EnvPassthrough                   []string
	HasEnvPassthrough                bool
	Setup                            []string
	HasSetup                         bool
	InheritSetup                     bool
	HasInheritSetup                  bool
	InheritEnvironmentPassthrough    bool
	HasInheritEnvironmentPassthrough bool
}

func decodeConfigArray(raw json.RawMessage, name string) ([]string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, ljaError("%s must be an array of nonempty strings", name)
	}
	var values []json.RawMessage
	if err := json.Unmarshal(trimmed, &values); err != nil {
		return nil, ljaError("%s must be an array of nonempty strings: %w", name, err)
	}
	decoded := make([]string, 0, len(values))
	for _, rawValue := range values {
		var value string
		if err := json.Unmarshal(rawValue, &value); err != nil || strings.TrimSpace(value) == "" || strings.IndexByte(value, 0) >= 0 {
			return nil, ljaError("%s must be an array of nonempty strings", name)
		}
		decoded = append(decoded, value)
	}
	return decoded, nil
}

func decodeConfigBool(raw json.RawMessage, name string) (bool, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || (trimmed[0] != 't' && trimmed[0] != 'f') {
		return false, ljaError("%s must be a boolean", name)
	}
	var value bool
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return false, ljaError("%s must be a boolean", name)
	}
	return value, nil
}

func decodeConfigEnvironment(raw json.RawMessage) (map[string]string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, ljaError("env must be an object with string values without NUL characters")
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &values); err != nil {
		return nil, ljaError("env must be an object with string values without NUL characters: %w", err)
	}
	decoded := make(map[string]string, len(values))
	for name, rawValue := range values {
		var value string
		if err := json.Unmarshal(rawValue, &value); err != nil || strings.IndexByte(value, 0) >= 0 {
			return nil, ljaError("env must be an object with string values without NUL characters")
		}
		decoded[name] = value
	}
	return decoded, nil
}

func reservedEnvironmentNames() map[string]bool {
	return map[string]bool{
		"CODEX_HOME":              true,
		"CLAUDE_CONFIG_DIR":       true,
		"OPENCODE_CONFIG_DIR":     true,
		"XDG_CONFIG_HOME":         true,
		"XDG_DATA_HOME":           true,
		"XDG_STATE_HOME":          true,
		"XDG_CACHE_HOME":          true,
		"OPENCODE_CONFIG_CONTENT": true,
	}
}

func decodeDevelopmentConfig(content []byte, isProject bool) (sourceDevelopmentConfig, error) {
	if !utf8.Valid(content) {
		return sourceDevelopmentConfig{}, ljaError("configuration is not valid UTF-8")
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(content, &values); err != nil || values == nil {
		if err != nil {
			return sourceDevelopmentConfig{}, ljaError("invalid JSON: %w", err)
		}
		return sourceDevelopmentConfig{}, ljaError("expected a JSON object")
	}
	allowed := map[string]bool{
		configPackages: true, configCopyGitConfig: true, configEnvironment: true,
		configEnvironmentPassthrough: true, configSetup: true,
	}
	if isProject {
		allowed[configInheritSetup] = true
		allowed[configInheritEnvironmentPassthrough] = true
	}
	unknown := make([]string, 0)
	for name := range values {
		if !allowed[name] {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) != 0 {
		sort.Strings(unknown)
		return sourceDevelopmentConfig{}, ljaError("unknown settings: %s", strings.Join(unknown, ", "))
	}
	decoded := sourceDevelopmentConfig{Env: make(map[string]string)}
	if raw, present := values[configPackages]; present {
		packages, err := decodeConfigArray(raw, configPackages)
		if err != nil {
			return sourceDevelopmentConfig{}, err
		}
		for _, packageName := range packages {
			if !packageNamePattern.MatchString(packageName) {
				return sourceDevelopmentConfig{}, ljaError("invalid package name: %s", packageName)
			}
		}
		decoded.Packages, decoded.HasPackages = packages, true
	}
	if raw, present := values[configCopyGitConfig]; present {
		value, err := decodeConfigBool(raw, configCopyGitConfig)
		if err != nil {
			return sourceDevelopmentConfig{}, err
		}
		decoded.CopyGitConfig, decoded.HasCopyGitConfig = value, true
	}
	if raw, present := values[configEnvironment]; present {
		environment, err := decodeConfigEnvironment(raw)
		if err != nil {
			return sourceDevelopmentConfig{}, err
		}
		decoded.Env, decoded.HasEnv = environment, true
	}
	if raw, present := values[configEnvironmentPassthrough]; present {
		passthrough, err := decodeConfigArray(raw, configEnvironmentPassthrough)
		if err != nil {
			return sourceDevelopmentConfig{}, err
		}
		decoded.EnvPassthrough, decoded.HasEnvPassthrough = passthrough, true
	}
	if raw, present := values[configSetup]; present {
		setup, err := decodeConfigArray(raw, configSetup)
		if err != nil {
			return sourceDevelopmentConfig{}, err
		}
		decoded.Setup, decoded.HasSetup = setup, true
	}
	if raw, present := values[configInheritSetup]; present {
		value, err := decodeConfigBool(raw, configInheritSetup)
		if err != nil {
			return sourceDevelopmentConfig{}, err
		}
		decoded.InheritSetup, decoded.HasInheritSetup = value, true
	}
	if raw, present := values[configInheritEnvironmentPassthrough]; present {
		value, err := decodeConfigBool(raw, configInheritEnvironmentPassthrough)
		if err != nil {
			return sourceDevelopmentConfig{}, err
		}
		decoded.InheritEnvironmentPassthrough, decoded.HasInheritEnvironmentPassthrough = value, true
	}
	reserved := reservedEnvironmentNames()
	for name := range decoded.Env {
		if !environmentNamePattern.MatchString(name) {
			return sourceDevelopmentConfig{}, ljaError("invalid environment variable name: %s", name)
		}
		if reserved[name] {
			return sourceDevelopmentConfig{}, ljaError("environment variable %s is managed by LJA", name)
		}
	}
	for _, name := range decoded.EnvPassthrough {
		if !environmentNamePattern.MatchString(name) {
			return sourceDevelopmentConfig{}, ljaError("invalid environment variable name: %s", name)
		}
		if reserved[name] {
			return sourceDevelopmentConfig{}, ljaError("environment variable %s is managed by LJA", name)
		}
	}
	return decoded, nil
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			unique = append(unique, value)
		}
	}
	return unique
}

func applyDevelopmentConfig(config DevelopmentConfig, source sourceDevelopmentConfig, path string) DevelopmentConfig {
	if source.HasPackages {
		config.Packages = uniqueStrings(source.Packages)
	}
	if source.HasCopyGitConfig {
		config.CopyGitConfig = source.CopyGitConfig
	}
	for name, value := range source.Env {
		config.Env[name] = value
	}
	if source.HasInheritEnvironmentPassthrough && !source.InheritEnvironmentPassthrough {
		config.EnvPassthrough = []string{}
	}
	if source.HasEnvPassthrough {
		config.EnvPassthrough = uniqueStrings(append(config.EnvPassthrough, source.EnvPassthrough...))
	}
	if source.HasInheritSetup && !source.InheritSetup {
		config.Setup = []SetupCommand{}
	}
	if source.HasSetup {
		for index, command := range source.Setup {
			config.Setup = append(config.Setup, SetupCommand{Source: path, Index: index + 1, Command: command})
		}
	}
	return config
}

func LoadDevelopmentConfig(project string) (DevelopmentConfig, error) {
	return loadDevelopmentConfig(project, environmentMap(), "")
}

func loadDevelopmentConfig(project string, environment map[string]string, hostHome string) (DevelopmentConfig, error) {
	canonicalProject, err := canonicalProjectPath(project)
	if err != nil {
		return DevelopmentConfig{}, err
	}
	if hostHome == "" {
		hostHome, err = homeDirectory()
		if err != nil {
			return DevelopmentConfig{}, err
		}
	}
	configHomeValue := environment[xdgConfigHomeEnv]
	if configHomeValue == "" {
		configHomeValue = filepath.Join(hostHome, defaultConfigDirectory)
	}
	configHome, err := canonicalPath(configHomeValue)
	if err != nil {
		return DevelopmentConfig{}, ljaError("cannot resolve configuration directory %s: %w", configHomeValue, err)
	}
	sources := []struct {
		path      string
		isProject bool
	}{
		{path: filepath.Join(configHome, programName, globalConfigName)},
		{path: filepath.Join(canonicalProject, projectConfigName), isProject: true},
	}
	config := DefaultDevelopmentConfig()
	for _, source := range sources {
		content, readErr := os.ReadFile(source.path)
		if readErr != nil {
			if os.IsNotExist(readErr) {
				slog.Debug("configuration not present", "source", source.path)
				continue
			}
			return DevelopmentConfig{}, ljaError("cannot read configuration %s: %w", source.path, readErr)
		}
		decoded, decodeErr := decodeDevelopmentConfig(content, source.isProject)
		if decodeErr != nil {
			return DevelopmentConfig{}, ljaError("invalid configuration %s: %w", source.path, decodeErr)
		}
		config = applyDevelopmentConfig(config, decoded, source.path)
	}
	return config, nil
}

func ValidateDevelopmentConfig(content []byte, isProject bool) error {
	_, err := decodeDevelopmentConfig(content, isProject)
	return err
}

func jsonConfiguration(config DevelopmentConfig) ([]byte, error) {
	encoded, err := json.MarshalIndent(config.AsJSON(), "", "  ")
	if err != nil {
		return nil, fmt.Errorf("cannot encode configuration: %w", err)
	}
	return append(encoded, '\n'), nil
}
