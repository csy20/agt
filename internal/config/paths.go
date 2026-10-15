package config

import (
	"os"
	"path/filepath"
	"runtime"
)

// Places is where agt keeps the token, the daemon log, and optional config.
// Data is AGT_DATA_DIR or the OS default. On Linux that default stays
// ~/.local/share/agt even when XDG_DATA_HOME is set, so an existing token
// does not move. Config and cache follow the OS convention.
type Places struct {
	Data   string
	Config string
	Cache  string
	Log    string
}

func CurrentPlaces() Places {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	loc := locations(runtime.GOOS, home, os.Getenv)
	data := env("AGT_DATA_DIR", loc.Data)
	cfg := env("AGT_CONFIG_DIR", loc.Config)
	cache := env("AGT_CACHE_DIR", loc.Cache)
	return Places{
		Data:   data,
		Config: cfg,
		Cache:  cache,
		Log:    filepath.Join(data, "agtd.log"),
	}
}

type place struct {
	Data   string
	Config string
	Cache  string
}

func locations(goos, home string, getenv func(string) string) place {
	if home == "" {
		fallback := filepath.Join(os.TempDir(), "agt")
		return place{Data: fallback, Config: fallback, Cache: fallback}
	}
	switch goos {
	case "darwin":
		support := filepath.Join(home, "Library", "Application Support", "agt")
		return place{
			Data:   support,
			Config: support,
			Cache:  filepath.Join(home, "Library", "Caches", "agt"),
		}
	case "windows":
		app := getenv("APPDATA")
		if app == "" {
			app = filepath.Join(home, "AppData", "Roaming")
		}
		local := getenv("LOCALAPPDATA")
		if local == "" {
			local = filepath.Join(home, "AppData", "Local")
		}
		return place{
			Data:   filepath.Join(app, "agt"),
			Config: filepath.Join(app, "agt"),
			Cache:  filepath.Join(local, "agt"),
		}
	default:
		configHome := getenv("XDG_CONFIG_HOME")
		if configHome == "" {
			configHome = filepath.Join(home, ".config")
		}
		cacheHome := getenv("XDG_CACHE_HOME")
		if cacheHome == "" {
			cacheHome = filepath.Join(home, ".cache")
		}
		// Do not honor XDG_DATA_HOME. The Linux token path is stable.
		return place{
			Data:   filepath.Join(home, ".local", "share", "agt"),
			Config: filepath.Join(configHome, "agt"),
			Cache:  filepath.Join(cacheHome, "agt"),
		}
	}
}
