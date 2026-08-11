package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultsRequireLoginAndModelSelection(t *testing.T) {
	cfg := Defaults()
	if cfg.Provider != "" || cfg.Model != "" {
		t.Fatalf("new-install defaults must not select a provider or model: %#v", cfg)
	}
}

func TestSavePersistsModel(t *testing.T) {
	dir := t.TempDir()
	cfg := Defaults()
	cfg.Model = "gpt-5.6-terra"
	if err := Save(dir, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil || got.Model != cfg.Model {
		t.Fatalf("model=%q err=%v", got.Model, err)
	}
}

func TestLoadMergesGlobalThenProjectConfiguration(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "global")
	t.Setenv("ATOM_HOME", global)
	if err := os.MkdirAll(filepath.Join(dir, ".atom"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(global, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(global, "config.json"), []byte(`{"model":"global-model","bash_timeout_seconds":30,"auto_load_skills":{"caveman":"full"}}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".atom", "config.json"), []byte(`{"model":"project-model","project_doc_max_bytes":65536}`), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "project-model" || cfg.BashTimeoutSeconds != 30 || cfg.ProjectDocMaxBytes != 65536 || cfg.AutoLoadSkills["caveman"] != "full" {
		t.Fatalf("merged config = %#v", cfg)
	}
}

func TestMigrateLocalConfigImportsLegacyDefaultsOnce(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "global")
	t.Setenv("ATOM_HOME", global)
	if err := os.MkdirAll(filepath.Join(dir, ".atom"), 0755); err != nil {
		t.Fatal(err)
	}
	legacy := Defaults()
	legacy.Provider, legacy.Model = "copilot", "gpt-5.6-luna"
	if err := Save(dir, legacy); err != nil {
		t.Fatal(err)
	}
	if err := MigrateLocalConfig(dir, legacy); err != nil {
		t.Fatal(err)
	}
	got, err := Load(t.TempDir())
	if err != nil || got.Provider != legacy.Provider || got.Model != legacy.Model {
		t.Fatalf("global config = %#v, err = %v", got, err)
	}
}
