package instructions

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Skill struct{ Name, Description, Path string }

func LoadAgents(workdir string) (string, []string, error) {
	root, err := workspaceRoot(workdir)
	if err != nil {
		return "", nil, err
	}
	var files []string
	var parts []string
	for dir := root; ; dir = filepath.Dir(dir) {
		path := filepath.Join(dir, "AGENTS.md")
		if b, err := os.ReadFile(path); err == nil {
			files = append(files, path)
			parts = append(parts, fmt.Sprintf("Instructions from %s:\n%s", path, string(b)))
		} else if !os.IsNotExist(err) {
			return "", nil, err
		}
		if filepath.Clean(dir) == filepath.Clean(workdir) {
			break
		}
	}
	return strings.Join(parts, "\n\n"), files, nil
}

func DiscoverSkills(workdir string) ([]Skill, error) {
	paths := []string{filepath.Join(workdir, ".atom", "skills")}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".atom", "skills"))
	}
	var out []Skill
	seen := map[string]bool{}
	for _, base := range paths {
		entries, err := os.ReadDir(base)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if !e.IsDir() || seen[e.Name()] {
				continue
			}
			p := filepath.Join(base, e.Name(), "SKILL.md")
			b, err := os.ReadFile(p)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			seen[e.Name()] = true
			out = append(out, Skill{Name: e.Name(), Description: firstLine(string(b)), Path: p})
		}
	}
	return out, nil
}

func LoadSkill(skills []Skill, name string) (string, error) {
	for _, s := range skills {
		if s.Name == name {
			b, err := os.ReadFile(s.Path)
			return string(b), err
		}
	}
	return "", fmt.Errorf("unknown skill %q", name)
}

func workspaceRoot(dir string) (string, error) {
	d, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	// A repository boundary prevents accidentally importing instructions from a
	// parent checkout while still supporting nested directories in one project.
	for p := d; ; p = filepath.Dir(p) {
		if _, err := os.Stat(filepath.Join(p, ".git")); err == nil {
			return p, nil
		}
		next := filepath.Dir(p)
		if next == p {
			break
		}
	}
	return d, nil
}
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "#"))
		if line != "" {
			return line
		}
	}
	return "No description."
}
func SkillCatalog(skills []Skill) string {
	if len(skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Available skills (load one only when relevant):\n")
	for _, s := range skills {
		fmt.Fprintf(&b, "- %s: %s\n", s.Name, s.Description)
	}
	return b.String()
}
