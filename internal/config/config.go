package config

import (
	"os"
	"path/filepath"
	"strconv"

	"github.com/csy20/agt/internal/profile"
)

// Config is process configuration. Model ids and prices are not compiled in
// as product constants beyond overridable defaults for the usage ledger.
type Config struct {
	ListenAddr       string
	DatabaseURL      string
	SandboxImage     string
	ImagePython      string
	ImageGo          string
	ImageNode        string
	DataDir          string
	Provider         string
	ModelBaseURL     string
	ModelAPIKey      string
	ModelName        string
	InputPerMillion  int64
	OutputPerMillion int64
	OTLPEndpoint     string
}

func Load() Config {
	python := env("AGT_IMAGE_PYTHON", "")
	sandbox := env("AGT_SANDBOX_IMAGE", "")
	if python == "" {
		python = sandbox
	}
	if python == "" {
		python = "agt-sandbox:python"
	}
	if sandbox == "" {
		sandbox = python
	}
	return Config{
		ListenAddr:       env("AGT_LISTEN", "127.0.0.1:8787"),
		DatabaseURL:      env("AGT_DATABASE_URL", "postgres://agt:agt@127.0.0.1:54329/agt?sslmode=disable"),
		SandboxImage:     sandbox,
		ImagePython:      python,
		ImageGo:          env("AGT_IMAGE_GO", "agt-sandbox:go"),
		ImageNode:        env("AGT_IMAGE_NODE", "agt-sandbox:node"),
		DataDir:          env("AGT_DATA_DIR", defaultDataDir()),
		Provider:         env("AGT_PROVIDER", "fake"),
		ModelBaseURL:     env("AGT_MODEL_BASE_URL", "https://api.openai.com/v1"),
		ModelAPIKey:      os.Getenv("AGT_MODEL_API_KEY"),
		ModelName:        os.Getenv("AGT_MODEL_NAME"),
		InputPerMillion:  dollarsPerMillion("AGT_INPUT_USD_PER_M", 0.10),
		OutputPerMillion: dollarsPerMillion("AGT_OUTPUT_USD_PER_M", 0.50),
		OTLPEndpoint:     os.Getenv("AGT_OTLP_ENDPOINT"),
	}
}

// ProfileImages returns the daemon-owned runtime tags. Empty fields use the
// built-in tags so a partially filled Config still fails closed at preflight
// instead of inventing a host command.
func (c Config) ProfileImages() profile.Images {
	python := c.ImagePython
	if python == "" {
		python = c.SandboxImage
	}
	if python == "" {
		python = "agt-sandbox:python"
	}
	goImage := c.ImageGo
	if goImage == "" {
		goImage = "agt-sandbox:go"
	}
	node := c.ImageNode
	if node == "" {
		node = "agt-sandbox:node"
	}
	return profile.Images{Python: python, Go: goImage, Node: node}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func dollarsPerMillion(key string, fallback float64) int64 {
	dollars := fallback
	if v := os.Getenv(key); v != "" {
		parsed, err := strconv.ParseFloat(v, 64)
		if err == nil && parsed >= 0 {
			dollars = parsed
		}
	}
	return int64(dollars * 1_000_000)
}

func defaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "agt")
	}
	return filepath.Join(home, ".local", "share", "agt")
}
