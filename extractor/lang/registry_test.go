package lang_test

import (
	"testing"

	"github.com/sjarmak/livedocs/extractor/lang"
)

func TestRegistryLookupByExtension(t *testing.T) {
	t.Parallel()
	reg := lang.NewRegistry()

	tests := []struct {
		ext      string
		wantLang string
		wantOK   bool
	}{
		{".ts", "typescript", true},
		{".tsx", "typescript", true},
		{".py", "python", true},
		{".sh", "shell", true},
		{".bash", "shell", true},
		{".go", "go", true},
		{".unknown", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.ext, func(t *testing.T) {
			t.Parallel()
			cfg, ok := reg.LookupByExtension(tt.ext)
			if ok != tt.wantOK {
				t.Fatalf("LookupByExtension(%q) ok = %v, want %v", tt.ext, ok, tt.wantOK)
			}
			if ok && cfg.Language != tt.wantLang {
				t.Errorf("LookupByExtension(%q).Language = %q, want %q", tt.ext, cfg.Language, tt.wantLang)
			}
		})
	}
}

func TestRegistryLookupByLanguage(t *testing.T) {
	t.Parallel()
	reg := lang.NewRegistry()

	for _, name := range []string{"typescript", "python", "shell", "go"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg, ok := reg.LookupByLanguage(name)
			if !ok {
				t.Fatalf("LookupByLanguage(%q) not found", name)
			}
			if cfg.Language != name {
				t.Errorf("Language = %q, want %q", cfg.Language, name)
			}
			if cfg.GrammarName == "" {
				t.Error("GrammarName is empty")
			}
		})
	}
}

func TestRegistryAllLanguages(t *testing.T) {
	t.Parallel()
	reg := lang.NewRegistry()
	all := reg.AllLanguages()
	if len(all) < 4 {
		t.Errorf("expected at least 4 languages, got %d", len(all))
	}
}
