package federation

import "strings"

const (
	maxHostLength            = 128
	maxSoftwareNameLength    = 32
	maxSoftwareVersionLength = 48
)

// SanitizeHost renders an untrusted remote host for inline code in Markdown.
// Only lowercase ASCII letters, digits, dots, hyphens, and colons survive;
// every other rune is replaced so the result can never break out of a code
// span or inject markup.
func SanitizeHost(raw string) string {
	return sanitizeValue(raw, true, func(r rune) bool {
		return isASCIILower(r) || isASCIIDigit(r) || r == '.' || r == '-' || r == ':'
	}, '?', maxHostLength)
}

// SanitizeSoftwareName renders an untrusted software name for inline code.
func SanitizeSoftwareName(raw *string) string {
	if raw == nil {
		return "unknown"
	}
	return sanitizeValue(*raw, true, func(r rune) bool {
		return isASCIILower(r) || isASCIIDigit(r) || r == '.' || r == '_' || r == '-' || r == '+'
	}, '_', maxSoftwareNameLength)
}

// SanitizeSoftwareVersion renders an untrusted software version for inline
// code. Unlike names, versions may legitimately contain uppercase letters and
// colons, so those stay intact.
func SanitizeSoftwareVersion(raw *string) string {
	if raw == nil {
		return "unknown"
	}
	return sanitizeValue(*raw, false, func(r rune) bool {
		return isASCIILower(r) || isASCIIUpper(r) || isASCIIDigit(r) ||
			r == '.' || r == '_' || r == '-' || r == '+' || r == ':'
	}, '_', maxSoftwareVersionLength)
}

func sanitizeValue(raw string, lowerASCII bool, allowed func(rune) bool, replacement rune, maxLength int) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "unknown"
	}

	var b strings.Builder
	b.Grow(min(len(trimmed), maxLength))
	for _, r := range trimmed {
		if lowerASCII && isASCIIUpper(r) {
			r += 'a' - 'A'
		}
		if allowed(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune(replacement)
		}
	}

	// Every accepted or replacement rune is ASCII, so byte slicing is safe.
	out := b.String()
	if len(out) > maxLength {
		out = out[:maxLength-3] + "..."
	}
	return out
}

func isASCIILower(r rune) bool { return r >= 'a' && r <= 'z' }

func isASCIIUpper(r rune) bool { return r >= 'A' && r <= 'Z' }

func isASCIIDigit(r rune) bool { return r >= '0' && r <= '9' }
