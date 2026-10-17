package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mewebstudio/conductor-cli/internal/agents"
	"github.com/mewebstudio/conductor-cli/internal/paths"
)

// Kind of search result.
const (
	KindSkill  = "skill"
	KindPlugin = "plugin"
)

// Result is one search hit.
type Result struct {
	Kind        string
	Name        string
	Source      string // skill: owner/repo; plugin: name@marketplace
	Description string
	Installs    int
	Origin      string // skills.sh, installed, marketplace:<name>, claude-directory
	Installed   bool
}

// RegistryURL is the public skills.sh search endpoint.
var RegistryURL = "https://skills.sh/api/search"

var client = &http.Client{Timeout: 20 * time.Second}

// SearchRegistry queries skills.sh. The endpoint is public but undocumented,
// so failures are reported, never fatal.
func SearchRegistry(ctx context.Context, query string) ([]Result, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, RegistryURL+"?q="+url.QueryEscape(query), nil)
	req.Header.Set("User-Agent", "conductor")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("skills.sh: HTTP %d", resp.StatusCode)
	}
	var body struct {
		Skills []struct {
			Source   string `json:"source"`
			SkillID  string `json:"skillId"`
			Name     string `json:"name"`
			Installs int    `json:"installs"`
		} `json:"skills"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("skills.sh: unexpected response: %w", err)
	}
	out := make([]Result, 0, len(body.Skills))
	for _, s := range body.Skills {
		name := s.SkillID
		if name == "" {
			name = s.Name
		}
		out = append(out, Result{Kind: KindSkill, Name: name, Source: s.Source, Installs: s.Installs, Origin: "skills.sh"})
	}
	return out, nil
}

// SearchInstalled matches skills already present in the shared folder and the
// Claude skills folder.
func SearchInstalled(query string) []Result {
	q := strings.ToLower(query)
	seen := map[string]bool{}
	var out []Result
	for _, dir := range []string{agents.SharedDir(), filepath.Join(paths.ClaudeDir(), "skills")} {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			name, desc := Frontmatter(filepath.Join(dir, e.Name(), "SKILL.md"))
			if name == "" {
				continue
			}
			if seen[name] || !(strings.Contains(strings.ToLower(name), q) || strings.Contains(strings.ToLower(desc), q)) {
				continue
			}
			seen[name] = true
			out = append(out, Result{Kind: KindSkill, Name: name, Description: desc, Origin: "installed", Installed: true})
		}
	}
	return out
}

// marketplaceManifest is the part of .claude-plugin/marketplace.json we read.
type marketplaceManifest struct {
	Name    string `json:"name"`
	Plugins []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"plugins"`
}

// SearchPlugins matches plugins in every marketplace Claude Code knows, plus
// Claude Code's cached plugin directory when present.
func SearchPlugins(query string) []Result {
	q := strings.ToLower(query)
	match := func(s ...string) bool {
		for _, x := range s {
			if strings.Contains(strings.ToLower(x), q) {
				return true
			}
		}
		return false
	}
	installed := InstalledPlugins()
	var out []Result
	seen := map[string]bool{}
	files, _ := filepath.Glob(filepath.Join(paths.ClaudeDir(), "plugins", "marketplaces", "*", ".claude-plugin", "marketplace.json"))
	for _, f := range files {
		var m marketplaceManifest
		b, err := os.ReadFile(f)
		if err != nil || json.Unmarshal(b, &m) != nil {
			continue
		}
		mk := m.Name
		if mk == "" {
			mk = filepath.Base(filepath.Dir(filepath.Dir(f)))
		}
		for _, p := range m.Plugins {
			if !match(p.Name, p.Description) {
				continue
			}
			id := p.Name + "@" + mk
			seen[p.Name] = true
			out = append(out, Result{Kind: KindPlugin, Name: p.Name, Source: id, Description: p.Description,
				Origin: "marketplace:" + mk, Installed: installed[id]})
		}
	}
	// Claude Code's directory cache (internal format; best effort).
	if b, err := os.ReadFile(filepath.Join(paths.ClaudeDir(), "plugins", "plugin-directory-cache-v2.json")); err == nil {
		var dir struct {
			Listings []struct {
				Name    string `json:"name"`
				Summary string `json:"summary"`
				Source  struct {
					Repository struct {
						URL string `json:"url"`
					} `json:"repository"`
				} `json:"source"`
			} `json:"listings"`
		}
		if json.Unmarshal(b, &dir) == nil {
			for _, l := range dir.Listings {
				if seen[l.Name] || !match(l.Name, l.Summary) {
					continue
				}
				seen[l.Name] = true
				out = append(out, Result{Kind: KindPlugin, Name: l.Name, Source: l.Source.Repository.URL,
					Description: l.Summary, Origin: "claude-directory"})
			}
		}
	}
	return out
}

// Search runs every source; registry errors come back separately.
func Search(ctx context.Context, query string, withPlugins bool) ([]Result, error) {
	local := SearchInstalled(query)
	have := map[string]bool{}
	for _, r := range local {
		have[r.Name] = true
	}
	remote, rerr := SearchRegistry(ctx, query)
	out := local
	for _, r := range remote {
		r.Installed = have[r.Name]
		if r.Installed {
			continue // already listed as installed
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Installed != out[j].Installed {
			return out[i].Installed
		}
		return out[i].Installs > out[j].Installs
	})
	if withPlugins {
		out = append(out, SearchPlugins(query)...)
	}
	return out, rerr
}
