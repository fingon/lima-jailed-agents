package lja

import (
	"bytes"
	"encoding/json"
	"strings"

	"go.yaml.in/yaml/v3"
)

const (
	limaMountsKey       = "mounts"
	limaBaseKey         = "base"
	limaImagesKey       = "images"
	limaDefaultTemplate = "template:default"
)

func decodeLimaConfig(node *yaml.Node) (map[string]any, error) {
	if _, err := configMapping(node, configLima); err != nil {
		return nil, err
	}
	value, err := decodeLimaValue(node)
	if err != nil {
		return nil, err
	}
	config, ok := value.(map[string]any)
	if !ok {
		return nil, configNodeError(node, "lima must be a mapping")
	}
	if _, present := config[limaMountsKey]; present {
		return nil, configNodeError(node, "lima.mounts is managed by LJA")
	}
	return config, nil
}

func decodeLimaValue(node *yaml.Node) (any, error) {
	switch node.Kind {
	case yaml.MappingNode:
		entries, err := configMapping(node, configLima)
		if err != nil {
			return nil, err
		}
		result := make(map[string]any, len(entries))
		for _, entry := range entries {
			value, err := decodeLimaValue(entry[1])
			if err != nil {
				return nil, err
			}
			result[entry[0].Value] = value
		}
		return result, nil
	case yaml.SequenceNode:
		result := make([]any, 0, len(node.Content))
		for _, item := range node.Content {
			value, err := decodeLimaValue(item)
			if err != nil {
				return nil, err
			}
			result = append(result, value)
		}
		return result, nil
	case yaml.ScalarNode:
		if node.Tag != yamlStringTag && node.Tag != yamlBoolTag && node.Tag != "!!int" && node.Tag != "!!float" {
			return nil, configNodeError(node, "lima values must be strings, booleans, numbers, lists, or mappings")
		}
		if strings.IndexByte(node.Value, 0) >= 0 {
			return nil, configNodeError(node, "lima values must not contain NUL characters")
		}
		var value any
		if err := node.Decode(&value); err != nil {
			return nil, configNodeError(node, "invalid lima value: %v", err)
		}
		if _, err := json.Marshal(value); err != nil {
			return nil, configNodeError(node, "invalid lima value: %v", err)
		}
		return value, nil
	default:
		return nil, configNodeError(node, "lima aliases and custom YAML nodes are not supported")
	}
}

func cloneLimaValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		return mergeLimaConfig(nil, value)
	case []any:
		result := make([]any, len(value))
		for index, item := range value {
			result[index] = cloneLimaValue(item)
		}
		return result
	default:
		return value
	}
}

func mergeLimaConfig(base, override map[string]any) map[string]any {
	result := make(map[string]any, len(base)+len(override))
	for key, value := range base {
		result[key] = cloneLimaValue(value)
	}
	for key, value := range override {
		if mapping, ok := value.(map[string]any); ok {
			inherited, ok := result[key].(map[string]any)
			if !ok {
				inherited = nil
			}
			result[key] = mergeLimaConfig(inherited, mapping)
		} else {
			result[key] = cloneLimaValue(value)
		}
	}
	return result
}

func limaCreationInput(config map[string]any) ([]byte, error) {
	if _, present := config[limaMountsKey]; present {
		return nil, ljaError("lima.mounts is managed by LJA")
	}
	creationConfig := mergeLimaConfig(nil, config)
	_, hasBase := creationConfig[limaBaseKey]
	_, hasImages := creationConfig[limaImagesKey]
	if !hasBase && !hasImages {
		creationConfig[limaBaseKey] = limaDefaultTemplate
	}
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(creationConfig); err != nil {
		return nil, ljaError("cannot encode Lima creation input: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, ljaError("cannot close Lima creation input encoder: %w", err)
	}
	return output.Bytes(), nil
}
