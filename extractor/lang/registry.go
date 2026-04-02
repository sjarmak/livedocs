package lang

// Registry maps file extensions and language names to their tree-sitter
// configurations and claim mappings.
type Registry struct {
	byExtension map[string]*Config
	byLanguage  map[string]*Config
}

// NewRegistry returns a Registry pre-populated with mappings for
// Go, TypeScript, Python, and Shell.
func NewRegistry() *Registry {
	configs := []*Config{
		goConfig(),
		typescriptConfig(),
		pythonConfig(),
		shellConfig(),
	}

	r := &Registry{
		byExtension: make(map[string]*Config),
		byLanguage:  make(map[string]*Config),
	}
	for _, cfg := range configs {
		r.byLanguage[cfg.Language] = cfg
		for _, ext := range cfg.Extensions {
			r.byExtension[ext] = cfg
		}
	}
	return r
}

// LookupByExtension returns the language config for a file extension.
func (r *Registry) LookupByExtension(ext string) (Config, bool) {
	cfg, ok := r.byExtension[ext]
	if !ok {
		return Config{}, false
	}
	return *cfg, true
}

// LookupByLanguage returns the language config for a language name.
func (r *Registry) LookupByLanguage(name string) (Config, bool) {
	cfg, ok := r.byLanguage[name]
	if !ok {
		return Config{}, false
	}
	return *cfg, true
}

// AllLanguages returns all registered language names.
func (r *Registry) AllLanguages() []string {
	names := make([]string, 0, len(r.byLanguage))
	for name := range r.byLanguage {
		names = append(names, name)
	}
	return names
}
