package security

import (
	"regexp"
	"strings"
)

var credentialPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)("(?:password|token|cookie|authorization|activationKey|activationCode|connectionKey|sessionSecret)"\s*:\s*)"[^"]*"`),
	regexp.MustCompile(`(?i)([?&](?:password|token|cookie|authorization|connection[_-]?key|activation[_-]?(?:key|code))=)[^&#\s]*`),
	regexp.MustCompile(`(?im)\b(authorization|cookie|set-cookie)\s*:\s*[^\r\n]+`),
	regexp.MustCompile(`(?i)\b(password|passwd|token|cookie|authorization|connection[_-]?key|activation[_-]?(?:key|code)|session[_-]?secret)\b(["']?\s*[:=]\s*["']?)[^\s,"'&}\]]+`),
}

func Redact(value string) string {
	result := strings.ReplaceAll(strings.ReplaceAll(value, "\r", `\r`), "\n", `\n`)
	for _, pattern := range credentialPatterns {
		result = pattern.ReplaceAllString(result, `${1}[FILTERED]`)
	}
	return strings.TrimSpace(result)
}
