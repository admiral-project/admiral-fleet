// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package security

import (
	"fmt"
	"regexp"
	"strings"
)

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)password=.*?\s`),
	regexp.MustCompile(`(?i)MYSQL_PWD=.*?\s`),
	regexp.MustCompile(`(?i)PGPASSWORD=.*?\s`),
	regexp.MustCompile(`(?i)token=.*?\s`),
	regexp.MustCompile(`(?i)secret=.*?\s`),
	regexp.MustCompile(`(?i)apikey=.*?\s`),
	regexp.MustCompile(`(?i)Authorization:.*?\s`),

	// Terminating patterns (at the end of string)
	regexp.MustCompile(`(?i)password=.*$`),
	regexp.MustCompile(`(?i)MYSQL_PWD=.*$`),
	regexp.MustCompile(`(?i)PGPASSWORD=.*$`),
	regexp.MustCompile(`(?i)token=.*$`),
	regexp.MustCompile(`(?i)secret=.*$`),
	regexp.MustCompile(`(?i)apikey=.*$`),
	regexp.MustCompile(`(?i)Authorization:.*$`),

	// -p password
	regexp.MustCompile(`(?i)-p\s.*?\s`),
	regexp.MustCompile(`(?i)-p\s.*$`),
	regexp.MustCompile(`(?i)-p[^\s].*?\s`),
	regexp.MustCompile(`(?i)-p[^\s].*$`),
}

func Sanitize(text string) string {
	for _, pattern := range secretPatterns {
		text = pattern.ReplaceAllStringFunc(text, func(match string) string {
			if strings.Contains(match, "=") {
				parts := strings.SplitN(match, "=", 2)
				suffix := ""
				if strings.HasSuffix(parts[1], " ") {
					suffix = " "
				}
				return parts[0] + "=[REDACTED]" + suffix
			}
			if strings.Contains(match, ":") {
				parts := strings.SplitN(match, ":", 2)
				suffix := ""
				if strings.HasSuffix(parts[1], " ") {
					suffix = " "
				}
				return parts[0] + ": [REDACTED]" + suffix
			}
			if strings.HasPrefix(match, "-p ") {
				suffix := ""
				if strings.HasSuffix(match, " ") {
					suffix = " "
				}
				return "-p [REDACTED]" + suffix
			}
			if strings.HasPrefix(match, "-p") {
				suffix := ""
				if strings.HasSuffix(match, " ") {
					suffix = " "
				}
				return "-p[REDACTED]" + suffix
			}
			return "[REDACTED]"
		})
	}
	return text
}

func SanitizeArgs(args []string) []string {
	sanitized := make([]string, len(args))
	for i, arg := range args {
		sanitized[i] = Sanitize(arg)
	}
	return sanitized
}

func ValidateRunArgs(args []string) error {
	for _, arg := range args {
		if strings.ContainsAny(arg, ";|\n") {
			return fmt.Errorf("arg contains shell metacharacter")
		}
		if strings.Contains(arg, "$(") || strings.Contains(arg, "`") {
			return fmt.Errorf("arg contains command substitution")
		}
		if strings.Contains(arg, "..") {
			return fmt.Errorf("arg contains path traversal")
		}
	}
	return nil
}

func ValidateExecParams(name string, args []string) error {
	if name == "" {
		return fmt.Errorf("executable name cannot be empty")
	}
	if strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("executable name contains path separator")
	}
	for _, arg := range args {
		if strings.ContainsAny(arg, ";|\n") {
			return fmt.Errorf("arg contains shell metacharacter")
		}
		if strings.Contains(arg, "$(") || strings.Contains(arg, "`") {
			return fmt.Errorf("arg contains command substitution")
		}
	}
	return nil
}
