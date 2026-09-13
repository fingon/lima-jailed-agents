package lja

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
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
	yamlBoolTag                         = "!!bool"
	yamlMapTag                          = "!!map"
	yamlNullTag                         = "!!null"
	yamlSequenceTag                     = "!!seq"
	yamlStringTag                       = "!!str"
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

type developmentConfigYAML struct {
	Packages       []string          `yaml:"packages"`
	CopyGitConfig  bool              `yaml:"copy_git_config"`
	Env            map[string]string `yaml:"env"`
	EnvPassthrough []string          `yaml:"env_passthrough"`
	Setup          []string          `yaml:"setup"`
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

func (config DevelopmentConfig) AsYAML() developmentConfigYAML {
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
	return developmentConfigYAML{
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

func configNodeError(node *yaml.Node, format string, arguments ...any) error {
	message := fmt.Sprintf(format, arguments...)
	if node == nil || node.Line <= 0 {
		return ljaError("%s", message)
	}
	if node.Column <= 0 {
		return ljaError("%s at line %d", message, node.Line)
	}
	return ljaError("%s at line %d, column %d", message, node.Line, node.Column)
}

func isYAMLNull(node *yaml.Node) bool {
	return node != nil && node.Kind == yaml.ScalarNode && node.Tag == yamlNullTag
}

func configMapping(node *yaml.Node, name string) ([][2]*yaml.Node, error) {
	if node == nil || node.Kind != yaml.MappingNode || node.Tag != yamlMapTag {
		if isYAMLNull(node) {
			return nil, configNodeError(node, "%s must not be null", name)
		}
		return nil, configNodeError(node, "%s must be a mapping", name)
	}
	if len(node.Content)%2 != 0 {
		return nil, configNodeError(node, "%s has an invalid mapping", name)
	}
	entries := make([][2]*yaml.Node, 0, len(node.Content)/2)
	seen := make(map[string]bool, len(node.Content)/2)
	for index := 0; index < len(node.Content); index += 2 {
		key := node.Content[index]
		value := node.Content[index+1]
		if key == nil || key.Kind != yaml.ScalarNode || key.Tag != yamlStringTag {
			return nil, configNodeError(key, "%s keys must be strings", name)
		}
		if strings.IndexByte(key.Value, 0) >= 0 {
			return nil, configNodeError(key, "%s keys must not contain NUL characters", name)
		}
		if seen[key.Value] {
			return nil, configNodeError(key, "duplicate %s key: %s", name, key.Value)
		}
		seen[key.Value] = true
		entries = append(entries, [2]*yaml.Node{key, value})
	}
	return entries, nil
}

func decodeConfigString(node *yaml.Node, name string, requireNonempty bool) (string, error) {
	if node == nil || node.Kind != yaml.ScalarNode || node.Tag != yamlStringTag {
		if isYAMLNull(node) {
			return "", configNodeError(node, "%s must not be null", name)
		}
		return "", configNodeError(node, "%s must be a string", name)
	}
	if strings.IndexByte(node.Value, 0) >= 0 {
		return "", configNodeError(node, "%s must not contain NUL characters", name)
	}
	if requireNonempty && strings.TrimSpace(node.Value) == "" {
		return "", configNodeError(node, "%s must be an array of nonempty strings", name)
	}
	return node.Value, nil
}

func decodeConfigArray(node *yaml.Node, name string) ([]string, error) {
	if node == nil || node.Kind != yaml.SequenceNode || node.Tag != yamlSequenceTag {
		if isYAMLNull(node) {
			return nil, configNodeError(node, "%s must not be null", name)
		}
		return nil, configNodeError(node, "%s must be an array of nonempty strings", name)
	}
	decoded := make([]string, 0, len(node.Content))
	for _, item := range node.Content {
		value, err := decodeConfigString(item, name, true)
		if err != nil {
			return nil, err
		}
		decoded = append(decoded, value)
	}
	return decoded, nil
}

func decodeConfigBool(node *yaml.Node, name string) (bool, error) {
	if node == nil || node.Kind != yaml.ScalarNode || node.Tag != yamlBoolTag {
		if isYAMLNull(node) {
			return false, configNodeError(node, "%s must not be null", name)
		}
		return false, configNodeError(node, "%s must be a boolean", name)
	}
	var value bool
	if err := node.Decode(&value); err != nil {
		return false, configNodeError(node, "%s must be a boolean: %v", name, err)
	}
	return value, nil
}

func decodeConfigEnvironment(node *yaml.Node) (map[string]string, error) {
	entries, err := configMapping(node, "env")
	if err != nil {
		return nil, err
	}
	reserved := reservedEnvironmentNames()
	decoded := make(map[string]string, len(entries))
	for _, entry := range entries {
		if err := validateEnvironmentName(entry[0], entry[0].Value, reserved); err != nil {
			return nil, err
		}
		value, valueErr := decodeConfigString(entry[1], "env values", false)
		if valueErr != nil {
			return nil, configNodeError(entry[1], "env must be a mapping with string values without NUL characters: %s", valueErr)
		}
		decoded[entry[0].Value] = value
	}
	return decoded, nil
}

func validateEnvironmentName(node *yaml.Node, name string, reserved map[string]bool) error {
	if !environmentNamePattern.MatchString(name) {
		return configNodeError(node, "invalid environment variable name: %s", name)
	}
	if reserved[name] {
		return configNodeError(node, "environment variable %s is managed by LJA", name)
	}
	return nil
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

func decodeYAMLDocument(content []byte) (*yaml.Node, error) {
	if !utf8.Valid(content) {
		return nil, ljaError("configuration is not valid UTF-8")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		if err == io.EOF {
			return nil, ljaError("configuration must contain a YAML document")
		}
		return nil, ljaError("invalid YAML: %w", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err == nil {
		return nil, configNodeError(&extra, "configuration must contain a single YAML document")
	} else if err != io.EOF {
		return nil, ljaError("invalid YAML: %w", err)
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 {
		return nil, configNodeError(&document, "configuration must contain a YAML mapping")
	}
	return document.Content[0], nil
}

func decodeDevelopmentConfig(content []byte, isProject bool) (sourceDevelopmentConfig, error) {
	root, err := decodeYAMLDocument(content)
	if err != nil {
		return sourceDevelopmentConfig{}, err
	}
	entries, err := configMapping(root, "configuration")
	if err != nil {
		return sourceDevelopmentConfig{}, err
	}
	allowed := map[string]bool{
		configPackages: true, configCopyGitConfig: true, configEnvironment: true,
		configEnvironmentPassthrough: true, configSetup: true,
	}
	if isProject {
		allowed[configInheritSetup] = true
		allowed[configInheritEnvironmentPassthrough] = true
	}
	values := make(map[string]*yaml.Node, len(entries))
	unknown := make([]string, 0)
	var unknownNode *yaml.Node
	for _, entry := range entries {
		name := entry[0].Value
		values[name] = entry[1]
		if !allowed[name] {
			unknown = append(unknown, name)
			if unknownNode == nil {
				unknownNode = entry[0]
			}
		}
	}
	if len(unknown) != 0 {
		sort.Strings(unknown)
		return sourceDevelopmentConfig{}, configNodeError(unknownNode, "unknown settings: %s", strings.Join(unknown, ", "))
	}
	decoded := sourceDevelopmentConfig{Env: make(map[string]string)}
	if node, present := values[configPackages]; present {
		packages, err := decodeConfigArray(node, configPackages)
		if err != nil {
			return sourceDevelopmentConfig{}, err
		}
		for index, packageName := range packages {
			if !packageNamePattern.MatchString(packageName) {
				return sourceDevelopmentConfig{}, configNodeError(node.Content[index], "invalid package name: %s", packageName)
			}
		}
		decoded.Packages, decoded.HasPackages = packages, true
	}
	if node, present := values[configCopyGitConfig]; present {
		value, err := decodeConfigBool(node, configCopyGitConfig)
		if err != nil {
			return sourceDevelopmentConfig{}, err
		}
		decoded.CopyGitConfig, decoded.HasCopyGitConfig = value, true
	}
	if node, present := values[configEnvironment]; present {
		environment, err := decodeConfigEnvironment(node)
		if err != nil {
			return sourceDevelopmentConfig{}, err
		}
		decoded.Env, decoded.HasEnv = environment, true
	}
	if node, present := values[configEnvironmentPassthrough]; present {
		passthrough, err := decodeConfigArray(node, configEnvironmentPassthrough)
		if err != nil {
			return sourceDevelopmentConfig{}, err
		}
		reserved := reservedEnvironmentNames()
		for index, name := range passthrough {
			if err := validateEnvironmentName(node.Content[index], name, reserved); err != nil {
				return sourceDevelopmentConfig{}, err
			}
		}
		decoded.EnvPassthrough, decoded.HasEnvPassthrough = passthrough, true
	}
	if node, present := values[configSetup]; present {
		setup, err := decodeConfigArray(node, configSetup)
		if err != nil {
			return sourceDevelopmentConfig{}, err
		}
		decoded.Setup, decoded.HasSetup = setup, true
	}
	if node, present := values[configInheritSetup]; present {
		value, err := decodeConfigBool(node, configInheritSetup)
		if err != nil {
			return sourceDevelopmentConfig{}, err
		}
		decoded.InheritSetup, decoded.HasInheritSetup = value, true
	}
	if node, present := values[configInheritEnvironmentPassthrough]; present {
		value, err := decodeConfigBool(node, configInheritEnvironmentPassthrough)
		if err != nil {
			return sourceDevelopmentConfig{}, err
		}
		decoded.InheritEnvironmentPassthrough, decoded.HasInheritEnvironmentPassthrough = value, true
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

func yamlConfiguration(config DevelopmentConfig) ([]byte, error) {
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(config.AsYAML()); err != nil {
		return nil, fmt.Errorf("cannot encode configuration: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("cannot close configuration encoder: %w", err)
	}
	return output.Bytes(), nil
}
