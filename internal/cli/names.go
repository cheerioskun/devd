package cli

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode"
)

func generatedWorkspaceName(seed string) (string, error) {
	random := make([]byte, 3)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate workspace name: %w", err)
	}
	return workspaceNameBase(seed) + "-" + hex.EncodeToString(random), nil
}

func workspaceNameBase(seed string) string {
	seed = strings.TrimSuffix(seed, "/")
	if slash := strings.LastIndex(seed, "/"); slash >= 0 {
		seed = seed[slash+1:]
	}
	if at := strings.Index(seed, "@"); at >= 0 {
		seed = seed[:at]
	}
	if colon := strings.LastIndex(seed, ":"); colon >= 0 {
		seed = seed[:colon]
	}

	var result strings.Builder
	previousDash := false
	for _, char := range strings.ToLower(seed) {
		if unicode.IsLetter(char) || unicode.IsDigit(char) {
			result.WriteRune(char)
			previousDash = false
			continue
		}
		if !previousDash && result.Len() > 0 {
			result.WriteByte('-')
			previousDash = true
		}
	}
	base := strings.Trim(result.String(), "-")
	if base == "" {
		base = "workspace"
	}
	if len(base) > 40 {
		base = strings.TrimRight(base[:40], "-")
	}
	return base
}
