package tools

import "testing"

func TestRemoteToolsAreNotAdvertised(t *testing.T) {
	for _, item := range Definitions() {
		fn := item.(map[string]any)["function"].(map[string]any)
		name := fn["name"].(string)
		if name == "git_push" || name == "pr_open" {
			t.Fatalf("advertised unsupported tool %s: %s", name, fn["description"])
		}
	}
}
