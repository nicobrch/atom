// Package instructions discovers AGENTS.md guidance and Agent Skills using the
// Codex-compatible local filesystem conventions.
package instructions

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const DefaultProjectDocMaxBytes = 32 * 1024
const maxSkillCatalogChars = 8000

type Options struct {
	Home                    string
	UserHome                string
	ProjectDocFallbackNames []string
	ProjectDocMaxBytes      int
	AdminSkillsDirectory    string
}

type Skill struct {
	Name, Description, Path, Scope string
}

func DefaultOptions() Options {
	home, _ := os.UserHomeDir()
	return Options{
		Home:                 filepath.Join(home, ".atom"),
		UserHome:             home,
		ProjectDocMaxBytes:   DefaultProjectDocMaxBytes,
		AdminSkillsDirectory: "/etc/atom/skills",
	}
}

// LoadAgents mirrors Codex's instruction discovery: a global override or base
// file, followed by at most one non-empty instruction file in every project
// directory from repository root through workdir. Later files have precedence.
func LoadAgents(workdir string, options Options) (string, []string, error) {
	options = normalizedOptions(options)
	wd, err := filepath.Abs(workdir)
	if err != nil {
		return "", nil, err
	}
	root, err := workspaceRoot(wd)
	if err != nil {
		return "", nil, err
	}
	parts, files, used := make([]string, 0), make([]string, 0), 0
	add := func(path string) (bool, bool, error) {
		b, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			return false, false, nil
		}
		if err != nil {
			return false, false, err
		}
		text := strings.TrimSpace(string(b))
		if text == "" {
			return false, false, nil
		}
		if used+len(b) > options.ProjectDocMaxBytes {
			return false, true, nil
		}
		parts, files, used = append(parts, text), append(files, path), used+len(b)
		return true, used >= options.ProjectDocMaxBytes, nil
	}
	for _, name := range []string{"AGENTS.override.md", "AGENTS.md"} {
		path := filepath.Join(options.Home, name)
		added, exhausted, err := add(path)
		if err != nil {
			return "", nil, fmt.Errorf("read %s: %w", path, err)
		}
		if exhausted {
			return strings.Join(parts, "\n\n"), files, nil
		}
		if added {
			break
		}
	}
project:
	for _, dir := range directories(root, wd) {
		for _, name := range append([]string{"AGENTS.override.md", "AGENTS.md"}, options.ProjectDocFallbackNames...) {
			path := filepath.Join(dir, name)
			added, exhausted, err := add(path)
			if err != nil {
				return "", nil, fmt.Errorf("read %s: %w", path, err)
			}
			if exhausted {
				break project
			}
			if added {
				break
			}
		}
	}
	return strings.Join(parts, "\n\n"), files, nil
}

// DiscoverSkills scans repository scopes nearest-first, then user and admin
// scopes. Duplicate names are intentionally retained, matching the Agent Skills
// standard; callers can use the path/scope to disambiguate a selection.
func DiscoverSkills(workdir string, options Options) ([]Skill, error) {
	options = normalizedOptions(options)
	wd, err := filepath.Abs(workdir)
	if err != nil {
		return nil, err
	}
	root, err := workspaceRoot(wd)
	if err != nil {
		return nil, err
	}
	var bases []skillBase
	for _, dir := range reverse(directories(root, wd)) {
		bases = append(bases, skillBase{filepath.Join(dir, ".agents", "skills"), "repository"})
	}
	if options.UserHome != "" {
		bases = append(bases, skillBase{filepath.Join(options.UserHome, ".agents", "skills"), "user"})
	}
	if options.AdminSkillsDirectory != "" {
		bases = append(bases, skillBase{options.AdminSkillsDirectory, "admin"})
	}
	var out []Skill
	for _, base := range bases {
		entries, err := os.ReadDir(base.path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read skills in %s: %w", base.path, err)
		}
		for _, entry := range entries {
			skillDir := filepath.Join(base.path, entry.Name())
			if !entry.IsDir() {
				info, err := os.Stat(skillDir) // Follow a supported symlinked skill folder.
				if err != nil || !info.IsDir() {
					continue
				}
			}
			if entry.Name() == "." || entry.Name() == ".." {
				continue
			}
			path := filepath.Join(skillDir, "SKILL.md")
			b, err := os.ReadFile(path)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("read %s: %w", path, err)
			}
			name, description := skillMetadata(string(b))
			if name == "" || description == "" {
				continue // A SKILL.md without required metadata is not a valid skill.
			}
			out = append(out, Skill{Name: name, Description: description, Path: path, Scope: base.scope})
		}
	}
	return out, nil
}

func LoadSkill(skills []Skill, name string) (string, error) {
	var matched []Skill
	for _, skill := range skills {
		if skill.Name == name || skill.Path == name {
			matched = append(matched, skill)
		}
	}
	if len(matched) == 0 {
		return "", fmt.Errorf("unknown skill %q", name)
	}
	if len(matched) > 1 {
		paths := make([]string, len(matched))
		for i, skill := range matched {
			paths[i] = skill.Path
		}
		return "", fmt.Errorf("skill %q is ambiguous; select one by path: %s", name, strings.Join(paths, ", "))
	}
	b, err := os.ReadFile(matched[0].Path)
	return string(b), err
}

func SkillCatalog(skills []Skill) string {
	if len(skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Available skills (read a skill's SKILL.md before using it):\n")
	for _, skill := range skills {
		line := fmt.Sprintf("- %s: %s (scope: %s; path: %s)\n", skill.Name, skill.Description, skill.Scope, skill.Path)
		if b.Len()+len(line) > maxSkillCatalogChars {
			remaining := maxSkillCatalogChars - b.Len()
			if remaining > len("- skills omitted; use /skills to inspect the full catalog.\n") {
				b.WriteString("- skills omitted; use /skills to inspect the full catalog.\n")
			}
			break
		}
		b.WriteString(line)
	}
	return b.String()
}

type skillBase struct{ path, scope string }

func normalizedOptions(options Options) Options {
	defaults := DefaultOptions()
	if options.Home == "" {
		options.Home = defaults.Home
	}
	if options.UserHome == "" {
		options.UserHome = defaults.UserHome
	}
	if options.ProjectDocMaxBytes <= 0 {
		options.ProjectDocMaxBytes = DefaultProjectDocMaxBytes
	}
	if options.AdminSkillsDirectory == "" {
		options.AdminSkillsDirectory = defaults.AdminSkillsDirectory
	}
	return options
}

func workspaceRoot(dir string) (string, error) {
	d, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
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

func directories(root, workdir string) []string {
	var dirs []string
	for dir := workdir; ; dir = filepath.Dir(dir) {
		dirs = append(dirs, dir)
		if filepath.Clean(dir) == filepath.Clean(root) {
			break
		}
	}
	return reverse(dirs)
}

func reverse[T any](in []T) []T {
	out := make([]T, len(in))
	for i := range in {
		out[len(in)-1-i] = in[i]
	}
	return out
}

func skillMetadata(contents string) (string, string) {
	scanner := bufio.NewScanner(strings.NewReader(contents))
	if !scanner.Scan() || strings.TrimSpace(scanner.Text()) != "---" {
		return "", ""
	}
	values := map[string]string{}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "---" {
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.Trim(strings.TrimSpace(value), "\"'")
		if key == "name" || key == "description" {
			values[key] = value
		}
	}
	return values["name"], values["description"]
}
