package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type DevContainer struct {
	Image             string `json:"image"`
	ForwardPorts      []int  `json:"forwardPorts"`
	PostCreateCommand string `json:"postCreateCommand"`
}

// removeJSONComments removes JSONC comments without touching comment-like text
// inside strings. Newlines are preserved so decoder errors retain useful line
// numbers.
func removeJSONComments(data []byte) []byte {
	result := append([]byte(nil), data...)
	inString := false
	escaped := false
	for i := 0; i < len(result); i++ {
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch result[i] {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		if result[i] == '"' {
			inString = true
			continue
		}
		if result[i] != '/' || i+1 >= len(result) {
			continue
		}
		switch result[i+1] {
		case '/':
			result[i], result[i+1] = ' ', ' '
			i += 2
			for ; i < len(result) && result[i] != '\n'; i++ {
				result[i] = ' '
			}
			i--
		case '*':
			result[i], result[i+1] = ' ', ' '
			i += 2
			for ; i < len(result); i++ {
				if i+1 < len(result) && result[i] == '*' && result[i+1] == '/' {
					result[i], result[i+1] = ' ', ' '
					i++
					break
				}
				if result[i] != '\n' {
					result[i] = ' '
				}
			}
		}
	}
	return result
}

func removeTrailingJSONCommas(data []byte) []byte {
	result := append([]byte(nil), data...)
	inString := false
	escaped := false
	for i := 0; i < len(result); i++ {
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch result[i] {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		if result[i] == '"' {
			inString = true
			continue
		}
		if result[i] != ',' {
			continue
		}
		for next := i + 1; next < len(result); next++ {
			switch result[next] {
			case ' ', '\t', '\r', '\n':
				continue
			case '}', ']':
				result[i] = ' '
			}
			break
		}
	}
	return result
}

func LoadDevContainer(dir string) (*DevContainer, error) {
	paths := []string{
		filepath.Join(dir, ".devcontainer", "devcontainer.json"),
		filepath.Join(dir, "devcontainer.json"),
	}

	var data []byte
	var path string
	for _, candidate := range paths {
		contents, err := os.ReadFile(candidate)
		if err == nil {
			data = contents
			path = candidate
			break
		}
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read %s: %w", candidate, err)
		}
	}
	if path == "" {
		return nil, nil
	}

	data = removeTrailingJSONCommas(removeJSONComments(data))
	var devContainer DevContainer
	if err := json.Unmarshal(data, &devContainer); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &devContainer, nil
}
