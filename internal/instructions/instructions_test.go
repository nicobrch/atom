package instructions

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadAgentsLayersGlobalAndProjectOverrides(t *testing.T) {
	root := t.TempDir()
	workdir := filepath.Join(root, "service", "api")
	atomHome := filepath.Join(root, "atom-home")
	mustWrite(t, filepath.Join(root, ".git"), "gitdir: nowhere")
	mustWrite(t, filepath.Join(atomHome, "AGENTS.md"), "global")
	mustWrite(t, filepath.Join(root, "AGENTS.md"), "root")
	mustWrite(t, filepath.Join(root, "service", "AGENTS.md"), "service")
	mustWrite(t, filepath.Join(workdir, "AGENTS.md"), "ignored")
	mustWrite(t, filepath.Join(workdir, "AGENTS.override.md"), "api override")

	text, files, err := LoadAgents(workdir, Options{Home: atomHome, ProjectDocMaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := text, "global\n\nroot\n\nservice\n\napi override"; got != want {
		t.Fatalf("instructions = %q, want %q", got, want)
	}
	if got, want := files, []string{filepath.Join(atomHome, "AGENTS.md"), filepath.Join(root, "AGENTS.md"), filepath.Join(root, "service", "AGENTS.md"), filepath.Join(workdir, "AGENTS.override.md")}; !reflect.DeepEqual(got, want) {
		t.Fatalf("files = %#v, want %#v", got, want)
	}
}

func TestLoadAgentsGlobalOverrideAndFallback(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, ".git"), "gitdir: nowhere")
	home := filepath.Join(root, "home")
	mustWrite(t, filepath.Join(home, "AGENTS.md"), "base")
	mustWrite(t, filepath.Join(home, "AGENTS.override.md"), "override")
	mustWrite(t, filepath.Join(root, "TEAM.md"), "team")
	text, _, err := LoadAgents(root, Options{Home: home, ProjectDocFallbackNames: []string{"TEAM.md"}, ProjectDocMaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if text != "override\n\nteam" {
		t.Fatalf("instructions = %q", text)
	}
}

func TestDiscoverSkillsUsesStandardScopesAndMetadata(t *testing.T) {
	root := t.TempDir()
	workdir := filepath.Join(root, "service")
	userHome, admin := filepath.Join(root, "user"), filepath.Join(root, "admin")
	mustWrite(t, filepath.Join(root, ".git"), "gitdir: nowhere")
	mustSkill(t, filepath.Join(root, ".agents", "skills", "root", "SKILL.md"), "root", "root skill")
	mustSkill(t, filepath.Join(workdir, ".agents", "skills", "local", "SKILL.md"), "local", "local skill")
	mustSkill(t, filepath.Join(userHome, ".agents", "skills", "user", "SKILL.md"), "user", "user skill")
	mustSkill(t, filepath.Join(admin, "admin", "SKILL.md"), "admin", "admin skill")

	skills, err := DiscoverSkills(workdir, Options{UserHome: userHome, AdminSkillsDirectory: admin})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, skill := range skills {
		names = append(names, skill.Name)
	}
	if want := []string{"local", "root", "user", "admin"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("skills = %v, want %v", names, want)
	}
	if skills[0].Scope != "repository" || skills[2].Scope != "user" || skills[3].Scope != "admin" {
		t.Fatalf("unexpected scopes: %#v", skills)
	}
}

func TestLoadSkillRejectsAmbiguousName(t *testing.T) {
	_, err := LoadSkill([]Skill{{Name: "review", Path: "one"}, {Name: "review", Path: "two"}}, "review")
	if err == nil {
		t.Fatal("LoadSkill accepted an ambiguous name")
	}
}

func mustSkill(t *testing.T, path, name, description string) {
	t.Helper()
	mustWrite(t, path, "---\nname: "+name+"\ndescription: "+description+"\n---\nInstructions")
}
func mustWrite(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
		t.Fatal(err)
	}
}
