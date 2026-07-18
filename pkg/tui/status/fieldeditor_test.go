package status

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/grovetools/flow/pkg/orchestration"
)

// writeTempJob writes a minimal job file with valid frontmatter and returns a
// *Job pointing at it (only the fields the field editor needs are populated).
func writeTempJob(t *testing.T) *orchestration.Job {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "job.md")
	content := "---\n" +
		"id: test-job\n" +
		"title: Test Job\n" +
		"status: pending\n" +
		"type: chat\n" +
		"---\n\nbody\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp job: %v", err)
	}
	return &orchestration.Job{ID: "test-job", Title: "Test Job", FilePath: path, Filename: "job.md", Status: orchestration.JobStatusPending, Type: orchestration.JobTypeChat}
}

// reloadFrontmatter re-reads and parses a job file's frontmatter.
func reloadFrontmatter(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read job file: %v", err)
	}
	fm, _, err := orchestration.ParseFrontmatter(data)
	if err != nil {
		t.Fatalf("parse frontmatter: %v", err)
	}
	return fm
}

// TestEnumTextDescriptorsRoundTrip commits a value for every enum/text
// descriptor via setJobFieldCmd and asserts it round-trips through
// ParseFrontmatter on the job file.
func TestEnumTextDescriptorsRoundTrip(t *testing.T) {
	for _, d := range jobFields {
		if d.Kind == fieldToggle {
			continue
		}
		t.Run(d.Key, func(t *testing.T) {
			job := writeTempJob(t)

			var value string
			if d.Kind == fieldEnum {
				// Use the last option (a non-default) — skip an empty "clear"
				// option so the assertion has a concrete value to check.
				value = d.Options[len(d.Options)-1]
				if value == "" && len(d.Options) > 1 {
					value = d.Options[len(d.Options)-2]
				}
			} else {
				value = "round-trip-" + d.Key
			}

			cmd := setJobFieldCmd([]*orchestration.Job{job}, d.Key, value)
			msg := cmd()
			if err, ok := msg.(error); ok {
				t.Fatalf("setJobFieldCmd returned error: %v", err)
			}
			if _, ok := msg.(RefreshMsg); !ok {
				t.Fatalf("setJobFieldCmd returned %T, want RefreshMsg", msg)
			}

			fm := reloadFrontmatter(t, job.FilePath)
			got := fmt.Sprint(fm[d.Key])
			if got != value {
				t.Errorf("field %q round-trip: frontmatter %q, want %q", d.Key, got, value)
			}
		})
	}
}

// TestToggleDescriptorsFlip asserts the memory/auto_complete toggles write a
// bool to frontmatter that round-trips.
func TestToggleDescriptorsFlip(t *testing.T) {
	for _, key := range []string{"memory", "auto_complete"} {
		t.Run(key, func(t *testing.T) {
			job := writeTempJob(t)
			for _, want := range []bool{true, false} {
				cmd := setJobFieldCmd([]*orchestration.Job{job}, key, want)
				if msg := cmd(); msg != (RefreshMsg{}) {
					t.Fatalf("toggle %q=%v returned %T (%v)", key, want, msg, msg)
				}
				fm := reloadFrontmatter(t, job.FilePath)
				got, ok := fm[key].(bool)
				if !ok {
					t.Fatalf("frontmatter %q = %v (%T), want bool", key, fm[key], fm[key])
				}
				if got != want {
					t.Errorf("toggle %q = %v, want %v", key, got, want)
				}
			}
		})
	}
}

// TestSchemaDriftGuard reflects the jsonschema `enum=` tags off orchestration.Job
// and asserts every enum descriptor whose struct field carries an enum tag has
// Options exactly matching that tag. It catches drift when someone changes the
// struct's accepted values without updating the hand-maintained descriptor table
// (and vice versa).
func TestSchemaDriftGuard(t *testing.T) {
	schemaEnums := jobSchemaEnums()

	checked := 0
	for _, d := range jobFields {
		if d.Kind != fieldEnum {
			continue
		}
		tagEnum, ok := schemaEnums[d.Key]
		if !ok {
			// No enum= on the struct (status/type/template are prose/hardcoded).
			continue
		}
		checked++
		if !reflect.DeepEqual(d.Options, tagEnum) {
			t.Errorf("descriptor %q Options %v drifted from jsonschema enum %v", d.Key, d.Options, tagEnum)
		}
	}
	// Guard the guard: the four schema-backed enums (provider/responder/
	// cache_ttl/cache_layout) must be exercised, or a silent tag rename would
	// make this test pass vacuously.
	if checked < 4 {
		t.Errorf("schema-drift guard only checked %d enum descriptors, want >=4", checked)
	}
}

// jobSchemaEnums parses the `enum=` entries from each orchestration.Job field's
// jsonschema struct tag, keyed by yaml field name.
func jobSchemaEnums() map[string][]string {
	out := map[string][]string{}
	rt := reflect.TypeOf(orchestration.Job{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		yamlName := strings.Split(f.Tag.Get("yaml"), ",")[0]
		if yamlName == "" || yamlName == "-" {
			continue
		}
		var enums []string
		for _, tok := range strings.Split(f.Tag.Get("jsonschema"), ",") {
			if strings.HasPrefix(tok, "enum=") {
				enums = append(enums, strings.TrimPrefix(tok, "enum="))
			}
		}
		if len(enums) > 0 {
			out[yamlName] = enums
		}
	}
	return out
}
