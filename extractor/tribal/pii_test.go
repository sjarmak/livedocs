package tribal

import (
	"testing"
)

func TestRedactPII(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		// Email patterns
		{
			name:  "simple email",
			input: "Contact alice@example.com for details",
			want:  "Contact [REDACTED_EMAIL] for details",
		},
		{
			name:  "email with plus addressing",
			input: "Send to user+tag@domain.co.uk please",
			want:  "Send to [REDACTED_EMAIL] please",
		},
		{
			name:  "email with dots",
			input: "first.last@sub.domain.org",
			want:  "[REDACTED_EMAIL]",
		},

		// Phone number patterns
		{
			name:  "US phone with dashes",
			input: "Call 555-123-4567 now",
			want:  "Call [REDACTED_PHONE] now",
		},
		{
			name:  "US phone with parens",
			input: "Call (555) 123-4567 now",
			want:  "Call [REDACTED_PHONE] now",
		},
		{
			name:  "US phone with dots",
			input: "Call 555.123.4567 now",
			want:  "Call [REDACTED_PHONE] now",
		},
		{
			name:  "international phone",
			input: "Call +44 20 7946 0958 for UK",
			want:  "Call [REDACTED_PHONE] for UK",
		},
		{
			name:  "intl phone with dashes",
			input: "Number: +1-800-555-0199",
			want:  "Number: [REDACTED_PHONE]",
		},

		// SSN patterns
		{
			name:  "SSN format",
			input: "SSN is 123-45-6789 on file",
			want:  "SSN is [REDACTED_SSN] on file",
		},
		{
			name:  "SSN at start",
			input: "123-45-6789 is the SSN",
			want:  "[REDACTED_SSN] is the SSN",
		},

		// IP address patterns
		{
			name:  "IPv4 address",
			input: "Server at 192.168.1.100 is down",
			want:  "Server at [REDACTED_IP] is down",
		},
		{
			name:  "localhost IP",
			input: "Use 127.0.0.1 for local",
			want:  "Use [REDACTED_IP] for local",
		},

		// API token patterns
		{
			name:  "OpenAI secret key",
			input: "key: sk-abc123def456ghi789jkl012mno",
			want:  "key: [REDACTED_TOKEN]",
		},
		{
			name:  "GitHub PAT",
			input: "token=ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef",
			want:  "token=[REDACTED_TOKEN]",
		},
		{
			name:  "AWS access key",
			input: "aws_key=AKIAIOSFODNN7EXAMPLE",
			want:  "aws_key=[REDACTED_TOKEN]",
		},

		// @mention patterns
		{
			name:  "mention at start of text",
			input: "@alice please review",
			want:  "[REDACTED_MENTION] please review",
		},
		{
			name:  "mention mid-sentence",
			input: "Hey @bob can you check",
			want:  "Hey [REDACTED_MENTION] can you check",
		},
		{
			name:  "mention with underscores",
			input: "cc @dev_team_lead",
			want:  "cc [REDACTED_MENTION]",
		},

		// No PII — passthrough
		{
			name:  "no PII plain text",
			input: "This is a normal sentence with no personal data.",
			want:  "This is a normal sentence with no personal data.",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "code snippet no PII",
			input: "func main() { fmt.Println(\"hello\") }",
			want:  "func main() { fmt.Println(\"hello\") }",
		},

		// Multi-pattern input
		{
			name:  "multiple PII types",
			input: "Email alice@corp.com, call 555-123-4567, SSN 123-45-6789, server 10.0.0.1, key sk-abcdefghijklmnopqrst0123, cc @reviewer",
			want:  "Email [REDACTED_EMAIL], call [REDACTED_PHONE], SSN [REDACTED_SSN], server [REDACTED_IP], key [REDACTED_TOKEN], cc [REDACTED_MENTION]",
		},
		{
			name:  "email not double-redacted as mention",
			input: "Send to user@example.com and @admin",
			want:  "Send to [REDACTED_EMAIL] and [REDACTED_MENTION]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RedactPII(tt.input)
			if got != tt.want {
				t.Errorf("RedactPII(%q)\n  got:  %q\n  want: %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestContainsPII(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{name: "email", input: "alice@example.com", want: true},
		{name: "phone US", input: "555-123-4567", want: true},
		{name: "phone intl", input: "+44 20 7946 0958", want: true},
		{name: "SSN", input: "123-45-6789", want: true},
		{name: "IP", input: "192.168.1.1", want: true},
		{name: "token sk", input: "sk-abc123def456ghi789jkl012mno", want: true},
		{name: "token ghp", input: "ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef", want: true},
		{name: "token AKIA", input: "AKIAIOSFODNN7EXAMPLE", want: true},
		{name: "mention", input: "hey @alice", want: true},
		{name: "plain text", input: "just a normal string", want: false},
		{name: "empty", input: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ContainsPII(tt.input)
			if got != tt.want {
				t.Errorf("ContainsPII(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestContainsPII_FalseAfterRedact(t *testing.T) {
	inputs := []string{
		"alice@example.com",
		"555-123-4567",
		"+44 20 7946 0958",
		"123-45-6789",
		"192.168.1.1",
		"sk-abc123def456ghi789jkl012mno",
		"ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef",
		"AKIAIOSFODNN7EXAMPLE",
		"hey @alice",
		"Email alice@corp.com, call 555-123-4567, SSN 123-45-6789, server 10.0.0.1, key sk-abcdefghijklmnopqrst0123, cc @reviewer",
	}

	for _, input := range inputs {
		redacted := RedactPII(input)
		if ContainsPII(redacted) {
			t.Errorf("ContainsPII should be false after RedactPII(%q), but got true. Redacted: %q", input, redacted)
		}
	}
}
