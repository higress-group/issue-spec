package templates

import (
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCheckedInSkillResourcesMatchTemplates(t *testing.T) {
	for _, skill := range IssueSpecSkillsWithOptions("higress-group/issue-spec", WorkflowAuthoringOptions{HTMLReviewEnabled: false}) {
		for _, resource := range skill.Resources {
			name := filepath.Join("..", "..", ".agents", "skills", skill.Name, resource.Path)
			body, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != resource.Content {
				t.Errorf("checked-in resource differs from template: %s", name)
			}
		}
	}
}

func TestSkillResourcesResolveWithoutLoadingOtherPhases(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		skills := IssueSpecSkillsWithOptions("owner/repo", WorkflowAuthoringOptions{HTMLReviewEnabled: enabled})
		files := map[string]string{}
		for _, skill := range skills {
			base := skill.Name + "/"
			files[base+"SKILL.md"] = skill.Content
			for _, resource := range skill.Resources {
				key := base + resource.Path
				if _, duplicate := files[key]; duplicate {
					t.Fatalf("duplicate generated resource: %s", key)
				}
				files[key] = resource.Content
			}
		}
		links := regexp.MustCompile(`\[[^\]]+\]\(([^)]+)\)`)
		for name, content := range files {
			if strings.Contains(name, "/assets/") {
				continue // Example placeholders are output assets, not instruction links.
			}
			for _, match := range links.FindAllStringSubmatch(content, -1) {
				target, err := url.Parse(match[1])
				if err != nil {
					t.Fatalf("invalid reference in %s: %v", name, err)
				}
				if target.IsAbs() || target.Path == "" {
					continue
				}
				resolved := path.Clean(path.Join(path.Dir(name), target.Path))
				if _, exists := files[resolved]; !exists {
					t.Errorf("enabled=%v: %s has unresolved link %s", enabled, name, resolved)
				}
			}
		}
		if !enabled {
			continue
		}
		shared := files["issue-spec-workflow/references/human-review-projections.md"]
		for _, phase := range []string{"proposal", "design", "implement"} {
			key := "issue-spec-workflow/references/projections/" + phase + ".md"
			if len(shared)+len(files[key]) > 18000 {
				t.Errorf("default %s projection context exceeds budget", phase)
			}
			if strings.Contains(files[key], "projection-example.md") || strings.Contains(files[key], "implementation-review.md") {
				t.Errorf("phase %s eagerly pulls unrelated instructions", phase)
			}
		}
	}
}

func TestRenderedSkillRoutingMetadataAndCommandParity(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		options := WorkflowAuthoringOptions{HTMLReviewEnabled: enabled}
		skills := IssueSpecSkillsWithOptions("owner/repo", options)
		for _, skill := range skills {
			parts := strings.SplitN(skill.Content, "---\n", 3)
			if len(parts) != 3 {
				t.Fatalf("missing frontmatter: %s", skill.Name)
			}
			var metadata struct {
				Name          string `yaml:"name"`
				Description   string `yaml:"description"`
				Compatibility string `yaml:"compatibility"`
			}
			if err := yaml.Unmarshal([]byte(parts[1]), &metadata); err != nil {
				t.Fatal(err)
			}
			if metadata.Name != skill.Name || metadata.Description == "" || metadata.Compatibility == "" || len(metadata.Description) > 180 {
				t.Fatalf("invalid or unbounded routing metadata: %+v", metadata)
			}
			if strings.Contains(skill.Content, "{{") {
				t.Errorf("unexpanded skill template: %s", skill.Name)
			}
		}
		for _, command := range IssueSpecCommandContentsWithOptions("owner/repo", options) {
			if strings.Contains(command.Body, "{{") || (command.ID == "apply" && !strings.Contains(command.Body, implementationReviewReference)) {
				t.Errorf("command-only %s delivery lost self-contained review instructions", command.ID)
			}
		}
	}
}
