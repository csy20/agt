package orch

import (
	"fmt"
	"os"

	"github.com/csy20/agt/internal/config"
	"github.com/csy20/agt/internal/model"
	"github.com/csy20/agt/internal/store"
)

// ProviderFor builds the adapter recorded on the run.
// A run with a snapshot keeps that endpoint and model. An empty snapshot is a
// row from before model configurations existed and uses the process settings
// for the provider already stored on the run. Neither path tries a second endpoint.
func ProviderFor(current store.Run, cfg config.Config) (model.Provider, error) {
	adapter := current.Provider
	if adapter == "" {
		adapter = "fake"
	}
	endpoint := current.ModelEndpoint
	name := current.ModelName
	keyEnv := current.CredentialEnv
	snap := current.PricingRecorded || current.ModelConfig != "" || current.ModelName != "" || current.ModelEndpoint != ""
	if !snap {
		switch adapter {
		case "fake":
			name = cfg.ModelName
		case "openai":
			endpoint = cfg.ModelBaseURL
			name = cfg.ModelName
			keyEnv = "AGT_MODEL_API_KEY"
		default:
			return nil, fmt.Errorf("unknown provider %s", adapter)
		}
	}
	switch adapter {
	case "fake":
		script, err := model.ParseScript(current.ScriptJSON)
		if err != nil {
			return nil, err
		}
		fake := model.NewFake(script)
		fake.ModelName = name
		return fake, nil
	case "openai":
		if endpoint == "" {
			return nil, fmt.Errorf("openai run %s has no recorded endpoint and was not sent to a hosted default", current.ID)
		}
		key := ""
		if keyEnv != "" {
			key = os.Getenv(keyEnv)
		}
		if key == "" && keyEnv == "AGT_MODEL_API_KEY" {
			key = cfg.ModelAPIKey
		}
		client := model.NewOpenAI(endpoint, key, name)
		if err := client.Validate(); err != nil {
			return nil, err
		}
		return client, nil
	default:
		return nil, fmt.Errorf("unknown provider %s", adapter)
	}
}
