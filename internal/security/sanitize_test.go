// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package security

import (
	"testing"
)

func TestSanitize(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"MySQL PWD", "MYSQL_PWD=supersecret", "MYSQL_PWD=[REDACTED]"},
		{"PGPassword", "PGPASSWORD=anothersecret", "PGPASSWORD=[REDACTED]"},
		{"Generic password", "password=123456", "password=[REDACTED]"},
		{"Token", "token=abc-123", "token=[REDACTED]"},
		{"API Key", "apikey=xyz-789", "apikey=[REDACTED]"},
		{"Authorization", "Authorization: Bearer mytoken", "Authorization: [REDACTED]"},
		{"Mixed case", "mYsQl_pWd=secret", "mYsQl_pWd=[REDACTED]"},
		{"No secret", "ls -la /tmp", "ls -la /tmp"},
		{"Short password flag", "mysql -u root -psecret", "mysql -u root -p[REDACTED]"},
		{"Short password flag with space", "mysql -u root -p secret", "mysql -u root -p [REDACTED]"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Sanitize(tt.input)
			if got != tt.expected {
				t.Errorf("Sanitize(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestSanitizeArgs(t *testing.T) {
	args := []string{"mysql", "-u", "root", "MYSQL_PWD=secret", "mydb"}
	expected := []string{"mysql", "-u", "root", "MYSQL_PWD=[REDACTED]", "mydb"}
	got := SanitizeArgs(args)

	if len(got) != len(expected) {
		t.Fatalf("Expected %d args, got %d", len(expected), len(got))
	}

	for i := range got {
		if got[i] != expected[i] {
			t.Errorf("Arg[%d] = %q, want %q", i, got[i], expected[i])
		}
	}
}

func TestValidateRunArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{"safe args", []string{"ls", "-la", "/tmp"}, false},
		{"shell metacharacter ;", []string{"ls", ";", "rm"}, true},
		{"shell metacharacter |", []string{"ls", "|", "grep"}, true},
		{"shell metacharacter newline", []string{"ls", "\n", "rm"}, true},
		{"command substitution $()", []string{"echo", "$(whoami)"}, true},
		{"command substitution backticks", []string{"echo", "`whoami`"}, true},
		{"path traversal", []string{"cat", "../../etc/passwd"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRunArgs(tt.args)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateRunArgs() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateExecParams(t *testing.T) {
	tests := []struct {
		name    string
		exe     string
		args    []string
		wantErr bool
	}{
		{"safe", "ls", []string{"-l"}, false},
		{"empty exe", "", []string{"-l"}, true},
		{"exe with path separator /", "/bin/ls", []string{"-l"}, true},
		{"exe with path separator \\", "C:\\bin\\ls", []string{"-l"}, true},
		{"arg with shell metacharacter", "ls", []string{"-l", ";"}, true},
		{"arg with command substitution", "ls", []string{"$(whoami)"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateExecParams(tt.exe, tt.args)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateExecParams() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
