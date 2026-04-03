package extractor

import "testing"

func TestIsSensitiveContent(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		expected bool
	}{
		{"empty string", "", false},
		{"whitespace only", "   ", false},
		{"normal text", "Manages pod lifecycle", false},
		{"password lowercase", "stores the database password", true},
		{"password uppercase", "DATABASE_PASSWORD is required", true},
		{"password mixed case", "Password reset handler", true},
		{"secret lowercase", "the secret key for signing", true},
		{"secret uppercase", "AWS_SECRET_ACCESS_KEY", true},
		{"token lowercase", "generates auth token", true},
		{"token uppercase", "REFRESH_TOKEN is expired", true},
		{"credential lowercase", "validates user credential", true},
		{"credential uppercase", "CREDENTIAL_STORE handles creds", true},
		{"api_key lowercase", "reads the api_key from env", true},
		{"api_key uppercase", "API_KEY must be set", true},
		{"no match similar", "Manages pod networking tokens are not here wait yes token", true},
		{"partial match tokenize", "tokenize the input", true},
		{"substring passwords", "passwords are hashed", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsSensitiveContent(tt.text)
			if got != tt.expected {
				t.Errorf("IsSensitiveContent(%q) = %v, want %v", tt.text, got, tt.expected)
			}
		})
	}
}

func TestFilterSensitiveClaims(t *testing.T) {
	claims := []Claim{
		{SubjectName: "PodManager", ObjectText: "Manages pod lifecycle", Predicate: PredicatePurpose},
		{SubjectName: "SecretStore", ObjectText: "Stores the secret key for signing", Predicate: PredicatePurpose},
		{SubjectName: "AuthHandler", ObjectText: "Validates auth token from request", Predicate: PredicatePurpose},
		{SubjectName: "Config", ObjectText: "Loads configuration from file", Predicate: PredicatePurpose},
		{SubjectName: "Creds", ObjectText: "Manages user credential rotation", Predicate: PredicatePurpose},
		{SubjectName: "KeyReader", ObjectText: "Reads the api_key from environment", Predicate: PredicatePurpose},
		{SubjectName: "Logger", ObjectText: "Writes structured logs to stdout", Predicate: PredicatePurpose},
	}

	filtered := FilterSensitiveClaims(claims)

	// Should keep: PodManager, Config, Logger (3 out of 7)
	if len(filtered) != 3 {
		t.Fatalf("expected 3 claims after filtering, got %d", len(filtered))
	}

	expected := map[string]bool{"PodManager": true, "Config": true, "Logger": true}
	for _, c := range filtered {
		if !expected[c.SubjectName] {
			t.Errorf("unexpected claim in filtered result: %s", c.SubjectName)
		}
	}

	// Verify original slice is not modified.
	if len(claims) != 7 {
		t.Error("original slice was modified")
	}
}

// TestSensitiveFilter is the acceptance-criteria entry point for the filter tests.
func TestSensitiveFilter(t *testing.T) {
	// Verify positive detection.
	positives := []string{
		"stores the database password",
		"reads the api_key from env",
		"generates auth token",
		"validates user credential",
		"the secret key for signing",
	}
	for _, text := range positives {
		if !IsSensitiveContent(text) {
			t.Errorf("expected IsSensitiveContent(%q) = true", text)
		}
	}

	// Verify negative detection.
	negatives := []string{
		"Manages pod lifecycle",
		"Loads configuration from file",
		"Writes structured logs to stdout",
	}
	for _, text := range negatives {
		if IsSensitiveContent(text) {
			t.Errorf("expected IsSensitiveContent(%q) = false", text)
		}
	}

	// Verify FilterSensitiveClaims removes sensitive claims.
	claims := []Claim{
		{SubjectName: "Safe", ObjectText: "Manages pods"},
		{SubjectName: "Unsafe", ObjectText: "Stores the password"},
	}
	filtered := FilterSensitiveClaims(claims)
	if len(filtered) != 1 {
		t.Fatalf("expected 1 claim after filter, got %d", len(filtered))
	}
	if filtered[0].SubjectName != "Safe" {
		t.Errorf("expected Safe claim, got %s", filtered[0].SubjectName)
	}
}

func TestFilterSensitiveClaims_EmptyInput(t *testing.T) {
	filtered := FilterSensitiveClaims(nil)
	if len(filtered) != 0 {
		t.Errorf("expected 0 claims for nil input, got %d", len(filtered))
	}

	filtered = FilterSensitiveClaims([]Claim{})
	if len(filtered) != 0 {
		t.Errorf("expected 0 claims for empty input, got %d", len(filtered))
	}
}
