package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/csy20/agt/internal/model"
	"github.com/csy20/agt/internal/redact"
)

const (
	// DefaultModelName is the legacy configuration built from AGT_PROVIDER and
	// AGT_MODEL_*. Named configurations do not inherit its endpoint.
	DefaultModelName = "default"
	modelFileName    = "model-default"
	// legacyOpenAIEndpoint is the historical base URL of the unnamed openai
	// configuration when AGT_MODEL_BASE_URL is unset. A failed call does not
	// switch to this URL, and a named configuration with an empty endpoint is
	// rejected instead of receiving it.
	legacyOpenAIEndpoint = "https://api.openai.com/v1"
)

// ModelConfig is one daemon-owned provider configuration.
// CredentialEnv is the name of an environment variable. The value is never stored here.
type ModelConfig struct {
	Name             string
	Adapter          string
	Endpoint         string
	Model            string
	InputPerMillion  int64
	OutputPerMillion int64
	CredentialEnv    string
	ScriptPath       string
	Scripted         bool
	Valid            bool
	Problem          string
}

// Catalog is the set of configurations the daemon will resolve.
// Selection never tries a later entry when the chosen one is missing or invalid.
type Catalog struct {
	Configs []ModelConfig
}

// Resolved is the snapshot stored on a new run.
type Resolved struct {
	Name             string
	Adapter          string
	Endpoint         string
	Model            string
	InputPerMillion  int64
	OutputPerMillion int64
	CredentialEnv    string
	ScriptPath       string
	Scripted         bool
}

// PublicModel is safe to return from the API and to write in a log line.
type PublicModel struct {
	Name          string `json:"name"`
	Adapter       string `json:"adapter"`
	Endpoint      string `json:"endpoint,omitempty"`
	Model         string `json:"model,omitempty"`
	InputUSDPerM  string `json:"input_usd_per_m"`
	OutputUSDPerM string `json:"output_usd_per_m"`
	CredentialEnv string `json:"credential_env,omitempty"`
	CredentialSet bool   `json:"credential_set"`
	Loopback      bool   `json:"loopback"`
	Scripted      bool   `json:"scripted"`
	Valid         bool   `json:"valid"`
	Problem       string `json:"problem,omitempty"`
	Selected      bool   `json:"selected"`
}

// LoadModels reads named configurations from the environment.
// AGT_MODELS is a comma-separated list. Each name uses AGT_MODEL_<NAME>_*.
// The legacy process variables always form the configuration named default.
func LoadModels() Catalog {
	return loadModels(os.Getenv)
}

func loadModels(getenv func(string) string) Catalog {
	configs := []ModelConfig{legacyConfig(getenv)}
	seen := map[string]bool{DefaultModelName: true}
	for _, raw := range strings.Split(getenv("AGT_MODELS"), ",") {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		cfg := namedConfig(name, getenv)
		if seen[cfg.Name] && cfg.Name != "" {
			cfg.Valid = false
			cfg.Problem = "duplicate model configuration " + cfg.Name
		}
		if cfg.Name != "" {
			seen[cfg.Name] = true
		}
		configs = append(configs, cfg)
	}
	return Catalog{Configs: configs}
}

// CatalogFrom returns a catalog for a process Config that was not loaded from the environment.
// Tests construct Config directly. The API key stays on the process and is not copied into the catalog.
func CatalogFrom(cfg Config) Catalog {
	if len(cfg.Models.Configs) > 0 {
		return cfg.Models
	}
	legacy := ModelConfig{
		Name:             DefaultModelName,
		Adapter:          cfg.Provider,
		Model:            cfg.ModelName,
		InputPerMillion:  cfg.InputPerMillion,
		OutputPerMillion: cfg.OutputPerMillion,
		CredentialEnv:    "AGT_MODEL_API_KEY",
	}
	if legacy.Adapter == "" {
		legacy.Adapter = "fake"
	}
	legacy.Scripted = legacy.Adapter == "fake"
	if legacy.Adapter == "openai" {
		legacy.Endpoint = cfg.ModelBaseURL
		if legacy.Endpoint == "" {
			legacy.Endpoint = legacyOpenAIEndpoint
		}
	}
	legacy.Problem = legacy.check(func(key string) string {
		if key == "AGT_MODEL_API_KEY" {
			return cfg.ModelAPIKey
		}
		return os.Getenv(key)
	})
	legacy.Valid = legacy.Problem == ""
	return Catalog{Configs: []ModelConfig{legacy}}
}

func legacyConfig(getenv func(string) string) ModelConfig {
	adapter := strings.TrimSpace(getenv("AGT_PROVIDER"))
	if adapter == "" {
		adapter = "fake"
	}
	cfg := ModelConfig{
		Name:             DefaultModelName,
		Adapter:          adapter,
		Model:            strings.TrimSpace(getenv("AGT_MODEL_NAME")),
		InputPerMillion:  dollarsPerMillionEnv(getenv, "AGT_INPUT_USD_PER_M", 0.10),
		OutputPerMillion: dollarsPerMillionEnv(getenv, "AGT_OUTPUT_USD_PER_M", 0.50),
		CredentialEnv:    "AGT_MODEL_API_KEY",
		Scripted:         adapter == "fake",
	}
	if adapter == "openai" {
		cfg.Endpoint = strings.TrimSpace(getenv("AGT_MODEL_BASE_URL"))
		if cfg.Endpoint == "" {
			cfg.Endpoint = legacyOpenAIEndpoint
		}
	}
	cfg.Problem = cfg.check(getenv)
	cfg.Valid = cfg.Problem == ""
	return cfg
}

func namedConfig(name string, getenv func(string) string) ModelConfig {
	cfg := ModelConfig{Name: name}
	if problem := checkName(name); problem != "" {
		cfg.Problem = problem
		return cfg
	}
	prefix := "AGT_MODEL_" + envKey(name) + "_"
	cfg.Adapter = strings.TrimSpace(getenv(prefix + "ADAPTER"))
	cfg.Endpoint = strings.TrimSpace(getenv(prefix + "ENDPOINT"))
	cfg.Model = strings.TrimSpace(getenv(prefix + "MODEL"))
	cfg.CredentialEnv = strings.TrimSpace(getenv(prefix + "API_KEY_ENV"))
	cfg.ScriptPath = strings.TrimSpace(getenv(prefix + "SCRIPT"))
	cfg.InputPerMillion = dollarsPerMillionEnv(getenv, prefix+"INPUT_USD_PER_M", 0)
	cfg.OutputPerMillion = dollarsPerMillionEnv(getenv, prefix+"OUTPUT_USD_PER_M", 0)
	cfg.Scripted = cfg.Adapter == "fake"
	cfg.Problem = cfg.check(getenv)
	cfg.Valid = cfg.Problem == ""
	return cfg
}

func (c ModelConfig) check(getenv func(string) string) string {
	if c.InputPerMillion < 0 || c.OutputPerMillion < 0 {
		return "configuration " + c.Name + " token prices must be zero or positive"
	}
	switch c.Adapter {
	case "fake":
		if c.Endpoint != "" {
			return "a scripted configuration has no endpoint"
		}
		return ""
	case "openai":
		if strings.TrimSpace(c.Model) == "" {
			return "openai configuration " + c.Name + " requires a model name"
		}
		if strings.TrimSpace(c.Endpoint) == "" {
			return "openai configuration " + c.Name + " requires an endpoint and was not given the hosted default"
		}
		u, err := url.Parse(c.Endpoint)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return "openai configuration " + c.Name + " endpoint must be an http or https URL"
		}
		if model.LoopbackURL(c.Endpoint) {
			return ""
		}
		if c.CredentialEnv == "" {
			return "openai configuration " + c.Name + " requires API_KEY_ENV for a non-loopback endpoint"
		}
		if strings.TrimSpace(getenv(c.CredentialEnv)) == "" {
			return "openai configuration " + c.Name + " credential " + c.CredentialEnv + " is unset"
		}
		return ""
	default:
		if c.Adapter == "" {
			return "configuration " + c.Name + " requires an adapter"
		}
		return "configuration " + c.Name + " adapter must be fake or openai"
	}
}

// Resolve returns one configuration. It does not substitute another name or endpoint.
func (c Catalog) Resolve(name string) (Resolved, error) {
	if strings.TrimSpace(name) == "" {
		name = DefaultModelName
	}
	cfg, ok := c.lookup(name)
	if !ok {
		return Resolved{}, fmt.Errorf("unknown model configuration %s", name)
	}
	if !cfg.Valid {
		problem := cfg.Problem
		if problem == "" {
			problem = "model configuration " + name + " is invalid"
		}
		return Resolved{}, fmt.Errorf("%s", problem)
	}
	return cfg.resolved(), nil
}

// Legacy resolves the historical single adapter from this catalog's default entry
// when the client names fake or openai directly. It still does not switch endpoints
// after a failure. An openai adapter uses only the default entry's recorded endpoint.
func (c Catalog) Legacy(adapter string) (Resolved, error) {
	cfg, ok := c.lookup(DefaultModelName)
	if !ok {
		return Resolved{}, fmt.Errorf("default model configuration is missing")
	}
	if adapter == cfg.Adapter {
		return c.Resolve(DefaultModelName)
	}
	if adapter == "fake" {
		out := Resolved{
			Name:             DefaultModelName,
			Adapter:          "fake",
			Model:            cfg.Model,
			InputPerMillion:  cfg.InputPerMillion,
			OutputPerMillion: cfg.OutputPerMillion,
			Scripted:         true,
		}
		return out, nil
	}
	if adapter != "openai" {
		return Resolved{}, fmt.Errorf("provider must be fake or openai")
	}
	endpoint := cfg.Endpoint
	if cfg.Adapter != "openai" {
		endpoint = ""
	}
	out := ModelConfig{
		Name:             DefaultModelName,
		Adapter:          "openai",
		Endpoint:         endpoint,
		Model:            cfg.Model,
		InputPerMillion:  cfg.InputPerMillion,
		OutputPerMillion: cfg.OutputPerMillion,
		CredentialEnv:    "AGT_MODEL_API_KEY",
	}
	if out.Endpoint == "" {
		out.Endpoint = legacyOpenAIEndpoint
	}
	problem := out.check(os.Getenv)
	if problem != "" {
		if out.Model == "" || !model.LoopbackURL(out.Endpoint) && strings.TrimSpace(os.Getenv("AGT_MODEL_API_KEY")) == "" {
			return Resolved{}, fmt.Errorf("AGT_MODEL_NAME is required for the openai provider. AGT_MODEL_API_KEY is required unless AGT_MODEL_BASE_URL is a loopback URL")
		}
		return Resolved{}, fmt.Errorf("%s", problem)
	}
	return out.resolved(), nil
}

func (c Catalog) lookup(name string) (ModelConfig, bool) {
	for _, cfg := range c.Configs {
		if cfg.Name == name {
			return cfg, true
		}
	}
	return ModelConfig{}, false
}

func (c ModelConfig) resolved() Resolved {
	return Resolved{
		Name:             c.Name,
		Adapter:          c.Adapter,
		Endpoint:         c.Endpoint,
		Model:            c.Model,
		InputPerMillion:  c.InputPerMillion,
		OutputPerMillion: c.OutputPerMillion,
		CredentialEnv:    c.CredentialEnv,
		ScriptPath:       c.ScriptPath,
		Scripted:         c.Scripted,
	}
}

// Public returns the catalog without credential values.
func (c Catalog) Public(selected string, getenv func(string) string) []PublicModel {
	secrets := c.Secrets(getenv)
	out := make([]PublicModel, 0, len(c.Configs))
	for _, cfg := range c.Configs {
		set := cfg.CredentialEnv != "" && strings.TrimSpace(getenv(cfg.CredentialEnv)) != ""
		item := PublicModel{
			Name:          cfg.Name,
			Adapter:       cfg.Adapter,
			Endpoint:      hideAll(cfg.Endpoint, secrets),
			Model:         hideAll(cfg.Model, secrets),
			InputUSDPerM:  formatUSDPerM(cfg.InputPerMillion),
			OutputUSDPerM: formatUSDPerM(cfg.OutputPerMillion),
			CredentialEnv: cfg.CredentialEnv,
			CredentialSet: set,
			Loopback:      model.LoopbackURL(cfg.Endpoint),
			Scripted:      cfg.Scripted,
			Valid:         cfg.Valid,
			Problem:       hideAll(cfg.Problem, secrets),
			Selected:      cfg.Name == selected,
		}
		out = append(out, item)
	}
	return out
}

// Secrets returns credential values currently present in the environment.
// The catalog stores only variable names.
func (c Catalog) Secrets(getenv func(string) string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			return
		}
		seen[v] = true
		out = append(out, v)
	}
	add(getenv("AGT_MODEL_API_KEY"))
	for _, cfg := range c.Configs {
		if cfg.CredentialEnv != "" {
			add(getenv(cfg.CredentialEnv))
		}
	}
	return out
}

// Hide removes catalog credentials from text that might be stored or returned.
func (c Catalog) Hide(text string, getenv func(string) string) string {
	return hideAll(text, c.Secrets(getenv))
}

func hideAll(s string, secrets []string) string {
	if s == "" {
		return ""
	}
	return redact.Hide(s, secrets)
}

// ReadSelectedModel returns the daemon default configuration name.
// An empty file or a missing file means default.
func ReadSelectedModel(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	body, err := os.ReadFile(filepath.Join(dataDir, modelFileName))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(body))
}

// WriteSelectedModel stores the daemon default. It does not call a model.
func WriteSelectedModel(dataDir, name string) error {
	if strings.TrimSpace(dataDir) == "" {
		return fmt.Errorf("data dir is required")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dataDir, modelFileName), []byte(name+"\n"), 0o600)
}

func (c ModelConfig) Credential(getenv func(string) string, processKey string) string {
	if c.CredentialEnv == "" {
		return ""
	}
	if v := strings.TrimSpace(getenv(c.CredentialEnv)); v != "" {
		return v
	}
	if c.CredentialEnv == "AGT_MODEL_API_KEY" {
		return processKey
	}
	return ""
}

func checkName(name string) string {
	if name == "" || len(name) > 32 {
		return "model configuration name must be 1 to 32 characters"
	}
	for i, r := range name {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-'
		if i == 0 {
			ok = r >= 'a' && r <= 'z'
		}
		if !ok || unicode.IsSpace(r) {
			return "model configuration name must be lowercase letters, digits, underscore, or hyphen"
		}
	}
	return ""
}

func envKey(name string) string {
	return strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
}

func dollarsPerMillionEnv(getenv func(string) string, key string, fallback float64) int64 {
	dollars := fallback
	if v := getenv(key); v != "" {
		parsed, err := strconv.ParseFloat(v, 64)
		if err != nil || parsed < 0 {
			return -1
		}
		dollars = parsed
	}
	return int64(dollars * 1_000_000)
}

func formatUSDPerM(micros int64) string {
	if micros < 0 {
		return "invalid"
	}
	sign := ""
	whole := micros / 1_000_000
	frac := micros % 1_000_000
	text := strconv.FormatInt(whole, 10) + "." + fmtFrac(frac)
	text = strings.TrimRight(text, "0")
	text = strings.TrimRight(text, ".")
	if text == "" {
		text = "0"
	}
	return sign + text
}

func fmtFrac(n int64) string {
	s := strconv.FormatInt(n, 10)
	for len(s) < 6 {
		s = "0" + s
	}
	return s
}
