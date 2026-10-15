package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func absolutePath(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", fmt.Errorf("empty path")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func resolveProvider(flagValue string) (string, error) {
	provider := strings.TrimSpace(flagValue)
	if provider == "" {
		provider = strings.TrimSpace(os.Getenv("AGT_PROVIDER"))
	}
	if provider == "" {
		return "", fmt.Errorf("no provider selected. Pass --provider fake --script FILE for the scripted demo, or --provider openai with AGT_MODEL_NAME and AGT_MODEL_API_KEY. AGT_PROVIDER is also accepted. agt does not default to the sample fixture")
	}
	switch provider {
	case "fake", "openai":
		return provider, nil
	default:
		return "", fmt.Errorf("provider must be fake or openai, got %q", provider)
	}
}

func prepareFakeScript(provider, script string) (string, error) {
	if provider != "fake" {
		if strings.TrimSpace(script) != "" {
			return "", fmt.Errorf("--script is only used with the fake demo provider")
		}
		return "", nil
	}
	if strings.TrimSpace(script) == "" {
		return "", fmt.Errorf("the fake provider is a scripted demo and requires --script FILE. The sample eval/scripts/fix_retry.json only solves the bundled payrepo retry fixture. It is not a live model and is not selected automatically")
	}
	abs, err := absolutePath(script)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(os.Stderr, "demo: scripted provider, not a live model (script %s)\n", abs)
	return abs, nil
}
