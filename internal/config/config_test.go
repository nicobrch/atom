package config

import "testing"

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
