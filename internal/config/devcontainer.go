package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

type DevContainer struct {
	Image             string `json:"image"`
	ForwardPorts      []int  `json:"forwardPorts"`
	PostCreateCommand string `json:"postCreateCommand"`
}

// removeComments strips // and /* */ comments from jsonc
func removeComments(data []byte) []byte {
	// Simple regex to remove block comments and line comments
	blockRegex := regexp.MustCompile(`(?s)/\*.*?\*/`)
	lineRegex := regexp.MustCompile(`//.*`)

	res := blockRegex.ReplaceAll(data, nil)
	res = lineRegex.ReplaceAll(res, nil)
	return res
}

func LoadDevContainer(dir string) (*DevContainer, error) {
	paths := []string{
		filepath.Join(dir, ".devcontainer", "devcontainer.json"),
		filepath.Join(dir, "devcontainer.json"),
	}

	var data []byte
	var err error
	for _, p := range paths {
		data, err = os.ReadFile(p)
		if err == nil {
			break
		}
	}

	if err != nil {
		return nil, nil // Not found, which is fine
	}

	data = removeComments(data)

	var dc DevContainer
	if err := json.Unmarshal(data, &dc); err != nil {
		return nil, fmt.Errorf("parse devcontainer.json: %w", err)
	}

	return &dc, nil
}
