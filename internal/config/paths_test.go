package config

import (
	"path/filepath"
	"testing"
)

func TestLinuxDataDirStaysBesideTheHomeShare(t *testing.T) {
	env := func(k string) string {
		if k == "XDG_DATA_HOME" {
			return "/custom/data"
		}
		if k == "XDG_CONFIG_HOME" {
			return "/custom/config"
		}
		return ""
	}
	loc := locations("linux", "/home/u", env)
	if loc.Data != filepath.Join("/home/u", ".local", "share", "agt") {
		t.Fatalf("data %s", loc.Data)
	}
	if loc.Config != filepath.Join("/custom/config", "agt") {
		t.Fatalf("config %s", loc.Config)
	}
	if loc.Cache != filepath.Join("/home/u", ".cache", "agt") {
		t.Fatalf("cache %s", loc.Cache)
	}
}

func TestDarwinAndWindowsUseTheirOwnDirectories(t *testing.T) {
	darwin := locations("darwin", "/Users/u", func(string) string { return "" })
	if darwin.Data != filepath.Join("/Users/u", "Library", "Application Support", "agt") {
		t.Fatalf("darwin data %s", darwin.Data)
	}
	if darwin.Cache != filepath.Join("/Users/u", "Library", "Caches", "agt") {
		t.Fatalf("darwin cache %s", darwin.Cache)
	}
	windows := locations("windows", `C:\Users\u`, func(k string) string {
		switch k {
		case "APPDATA":
			return `C:\Users\u\AppData\Roaming`
		case "LOCALAPPDATA":
			return `C:\Users\u\AppData\Local`
		default:
			return ""
		}
	})
	if windows.Data != filepath.Join(`C:\Users\u\AppData\Roaming`, "agt") {
		t.Fatalf("windows data %s", windows.Data)
	}
	if windows.Cache != filepath.Join(`C:\Users\u\AppData\Local`, "agt") {
		t.Fatalf("windows cache %s", windows.Cache)
	}
}

func TestMissingHomeFallsBackToTemp(t *testing.T) {
	loc := locations("linux", "", func(string) string { return "" })
	if loc.Data == "" || loc.Data == filepath.Join(".local", "share", "agt") {
		t.Fatalf("data %s", loc.Data)
	}
}
