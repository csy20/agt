package config

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestNamedModelsValidateAndHideCredentials(t *testing.T) {
	const secret = "sk-test-secret-value"
	getenv := func(key string) string {
		switch key {
		case "AGT_PROVIDER":
			return "openai"
		case "AGT_MODEL_NAME":
			return "legacy-model"
		case "AGT_MODELS":
			return "local,cloud,bare,priced,script,default"
		case "AGT_MODEL_LOCAL_ADAPTER":
			return "openai"
		case "AGT_MODEL_LOCAL_ENDPOINT":
			return "http://127.0.0.1:9/v1"
		case "AGT_MODEL_LOCAL_MODEL":
			return "local-repair"
		case "AGT_MODEL_LOCAL_INPUT_USD_PER_M":
			return "3"
		case "AGT_MODEL_CLOUD_ADAPTER":
			return "openai"
		case "AGT_MODEL_CLOUD_ENDPOINT":
			return "https://example.test/v1?access=" + secret
		case "AGT_MODEL_CLOUD_MODEL":
			return "cloud-model"
		case "AGT_MODEL_CLOUD_API_KEY_ENV":
			return "AGT_CLOUD_KEY"
		case "AGT_CLOUD_KEY":
			return secret
		case "AGT_MODEL_BARE_ADAPTER":
			return "openai"
		case "AGT_MODEL_BARE_MODEL":
			return "bare-model"
		case "AGT_MODEL_PRICED_ADAPTER":
			return "fake"
		case "AGT_MODEL_PRICED_INPUT_USD_PER_M":
			return "-1"
		case "AGT_MODEL_SCRIPT_ADAPTER":
			return "weird"
		case "AGT_MODEL_API_KEY":
			return secret
		default:
			return ""
		}
	}
	cat := loadModels(getenv)
	byName := map[string]ModelConfig{}
	for _, cfg := range cat.Configs {
		byName[cfg.Name] = cfg
	}
	local := byName["local"]
	if !local.Valid || local.Endpoint != "http://127.0.0.1:9/v1" || local.InputPerMillion != 3_000_000 {
		t.Fatalf("loopback config %+v", local)
	}
	if _, err := cat.Resolve("local"); err != nil {
		t.Fatal(err)
	}
	cloud := byName["cloud"]
	if !cloud.Valid || cloud.CredentialEnv != "AGT_CLOUD_KEY" {
		t.Fatalf("cloud %+v", cloud)
	}
	bare := byName["bare"]
	if bare.Valid || bare.Endpoint != "" || !strings.Contains(bare.Problem, "was not given the hosted default") {
		t.Fatalf("bare inherited a hosted endpoint: %+v", bare)
	}
	if _, err := cat.Resolve("bare"); err == nil || !strings.Contains(err.Error(), "was not given the hosted default") {
		t.Fatalf("bare resolve %v", err)
	}
	priced := byName["priced"]
	if priced.Valid || !strings.Contains(priced.Problem, "token prices") {
		t.Fatalf("priced %+v", priced)
	}
	script := byName["script"]
	if script.Valid || !strings.Contains(script.Problem, "adapter must be fake or openai") {
		t.Fatalf("adapter %+v", script)
	}
	legacyDefault := cat.Configs[0]
	if legacyDefault.Name != DefaultModelName || !legacyDefault.Valid || legacyDefault.Adapter != "openai" || legacyDefault.Endpoint != legacyOpenAIEndpoint {
		t.Fatalf("legacy default %+v", legacyDefault)
	}
	var duplicates int
	for _, cfg := range cat.Configs {
		if cfg.Name == DefaultModelName && strings.Contains(cfg.Problem, "duplicate") {
			duplicates++
		}
	}
	if duplicates != 1 {
		t.Fatalf("duplicate default count %d", duplicates)
	}

	raw, err := json.Marshal(cat.Public("local", getenv))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, secret) {
		t.Fatal("public catalog contained the credential")
	}
	if !strings.Contains(text, "AGT_CLOUD_KEY") || !strings.Contains(text, "[redacted]") {
		t.Fatalf("catalog %s", text)
	}
	for _, item := range cat.Public("local", getenv) {
		if item.Name != DefaultModelName && strings.Contains(item.Endpoint, "api.openai.com") {
			t.Fatalf("named config used the hosted default: %+v", item)
		}
	}

	fake := loadModels(func(key string) string {
		if key == "AGT_PROVIDER" {
			return "fake"
		}
		if key == "AGT_MODEL_NAME" {
			return "local-repair"
		}
		if key == "AGT_MODELS" {
			return "demo"
		}
		if key == "AGT_MODEL_DEMO_ADAPTER" {
			return "fake"
		}
		if key == "AGT_MODEL_DEMO_ENDPOINT" {
			return "http://127.0.0.1:9/v1"
		}
		return ""
	})
	for _, cfg := range fake.Configs {
		if cfg.Name == DefaultModelName {
			if !cfg.Scripted || !cfg.Valid || cfg.Endpoint != "" {
				t.Fatalf("scripted default %+v", cfg)
			}
		}
		if cfg.Name == "demo" && (cfg.Valid || !strings.Contains(cfg.Problem, "scripted configuration has no endpoint")) {
			t.Fatalf("demo %+v", cfg)
		}
	}

	legacy, err := cat.Legacy("openai")
	if err != nil || legacy.Endpoint != legacyOpenAIEndpoint || legacy.Model != "legacy-model" {
		t.Fatalf("legacy %+v %v", legacy, err)
	}
	noKey := loadModels(func(string) string { return "" })
	_, err = noKey.Legacy("openai")
	const want = "AGT_MODEL_NAME is required for the openai provider. AGT_MODEL_API_KEY is required unless AGT_MODEL_BASE_URL is a loopback URL"
	if err == nil || err.Error() != want {
		t.Fatalf("legacy error %v", err)
	}
	loop := loadModels(func(key string) string {
		switch key {
		case "AGT_PROVIDER":
			return "openai"
		case "AGT_MODEL_NAME":
			return "local-repair"
		case "AGT_MODEL_BASE_URL":
			return "http://127.0.0.1:9/v1"
		default:
			return ""
		}
	})
	resolved, err := loop.Resolve(DefaultModelName)
	if err != nil || resolved.Endpoint != "http://127.0.0.1:9/v1" || resolved.CredentialEnv != "AGT_MODEL_API_KEY" {
		t.Fatalf("loopback %+v %v", resolved, err)
	}
}

func TestSelectedModelFileIsPrivate(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if ReadSelectedModel(dir) != "" {
		t.Fatal("missing file was not the default")
	}
	if err := WriteSelectedModel(dir, "local"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir + "/model-default")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	if ReadSelectedModel(dir) != "local" {
		t.Fatalf("selected %q", ReadSelectedModel(dir))
	}
	if err := WriteSelectedModel("", "local"); err == nil {
		t.Fatal("empty data dir was accepted")
	}
}
