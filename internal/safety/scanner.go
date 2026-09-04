package safety

import (
	"regexp"
	"strings"
)

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)sk-[a-zA-Z0-9]{20,}`),
	regexp.MustCompile(`ghp_[a-zA-Z0-9]{20,}`),
	regexp.MustCompile(`gho_[a-zA-Z0-9]{20,}`),
	regexp.MustCompile(`-----BEGIN (?:RSA |EC |DSA )?PRIVATE KEY-----`),
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	regexp.MustCompile(`(?i)aws_secret_access_key\s*[:=]\s*\S+`),
	regexp.MustCompile(`(?i)api[_-]?key\s*[:=]\s*['"]?\S{20,}`),
}

var injectionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)ignore (?:all )?previous instructions`),
	regexp.MustCompile(`(?i)you are now (?:a |an )?`),
	regexp.MustCompile(`(?i)\[PALATE:`),
	regexp.MustCompile(`(?i)<\s*system\s*>`),
	regexp.MustCompile("\u200b|\u200c|\u200d|\u2060"),
	regexp.MustCompile("\u202a|\u202b|\u202c|\u202d|\u202e"),
}

func ContainsSecret(s string) bool {
	for _, re := range secretPatterns {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

func Censor(s string) string {
	out := s
	for _, re := range secretPatterns {
		out = re.ReplaceAllString(out, "[REDACTED]")
	}
	return out
}

type ScanResult struct {
	Flagged bool
	Reason  string
	Cleaned string
}

func Scan(s string) ScanResult {
	if ContainsSecret(s) {
		return ScanResult{Flagged: true, Reason: "secret", Cleaned: Censor(s)}
	}
	for _, re := range injectionPatterns {
		if re.MatchString(s) {
			cleaned := re.ReplaceAllString(s, "")
			cleaned = strings.TrimSpace(cleaned)
			return ScanResult{Flagged: true, Reason: "injection", Cleaned: "[PALATE: FLAGGED] " + cleaned}
		}
	}
	return ScanResult{Flagged: false, Cleaned: s}
}

func WrapUntrusted(content string) string {
	return "<UNTRUSTED_CONTENT>\n" + content + "\n</UNTRUSTED_CONTENT>"
}

func ScanAndWrap(s string) (string, ScanResult) {
	r := Scan(s)
	if r.Flagged {
		return WrapUntrusted(r.Cleaned), r
	}
	return s, r
}
