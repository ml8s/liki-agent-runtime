package agent

import (
	"errors"
	"testing"

	"github.com/ml8s/agent-runtime/internal/domain"
	"google.golang.org/adk/v2/model"
)

func TestModelRegistryBuildsEachNameOnce(t *testing.T) {
	t.Parallel()
	calls := 0
	build := func(_ string) (model.LLM, error) {
		calls++
		return &fakeLLM{}, nil
	}
	registry, err := newModelRegistryWithBuilder(build, []string{
		"glm-5.3-flash", "glm-5.3-flash", "gpt-4.1",
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := registry.resolve("glm-5.3-flash")
	if err != nil {
		t.Fatal(err)
	}
	second, err := registry.resolve("glm-5.3-flash")
	if err != nil {
		t.Fatal(err)
	}
	if first != second || calls != 2 {
		t.Fatalf("same name: same-instance=%v builds=%d, want one cached build per name", first == second, calls)
	}
	third, err := registry.resolve("gpt-4.1")
	if err != nil {
		t.Fatal(err)
	}
	if third == first || calls != 2 {
		t.Fatalf("distinct name: same-instance=%v builds=%d, want a distinct prebuilt client", third == first, calls)
	}
}

func TestModelRegistryResolvesTestOverride(t *testing.T) {
	t.Parallel()
	override := &fakeLLM{}
	buildCalls := 0
	registry, err := newModelRegistryWithBuilder(func(_ string) (model.LLM, error) {
		buildCalls++
		return override, nil
	}, []string{"glm-5.3-flash"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := registry.resolve("glm-5.3-flash")
	if err != nil {
		t.Fatal(err)
	}
	_, err = registry.resolve("missing")
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeLLMModelMissing {
		t.Fatalf("resolve(missing) error = %v, want %s", err, domain.CodeLLMModelMissing)
	}
	if first != override || buildCalls != 1 {
		t.Fatalf("override resolution = %#v builds=%d, want the prebuilt override", first, buildCalls)
	}
}

func TestModelRegistryRejectsUnknownModel(t *testing.T) {
	t.Parallel()
	registry := modelRegistry{models: map[string]model.LLM{}}
	_, err := registry.resolve("missing")
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeLLMModelMissing {
		t.Fatalf("resolve(missing) error = %v, want %s", err, domain.CodeLLMModelMissing)
	}
}
