package security

import (
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
