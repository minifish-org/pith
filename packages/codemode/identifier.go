package codemode

// ToIdentifier returns the identifier a script uses for a tool: characters that
// are not valid in a JavaScript identifier become "_". The first character must
// be a letter, "_" or "$". An empty name becomes "_".
func ToIdentifier(name string) string {
	out := make([]rune, 0, len(name))
	for _, r := range name {
		valid := false
		if len(out) == 0 {
			valid = isIdentifierStart(r)
		} else {
			valid = isIdentifierPart(r)
		}
		if valid {
			out = append(out, r)
		} else {
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "_"
	}
	return string(out)
}

func isIdentifierStart(r rune) bool {
	return r == '_' || r == '$' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')
}

func isIdentifierPart(r rune) bool {
	return isIdentifierStart(r) || (r >= '0' && r <= '9')
}

// isIdentifier reports whether name is a valid JavaScript identifier (ASCII,
// matching the frozen wrapper's IDENTIFIER regex).
func isIdentifier(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if i == 0 {
			if !isIdentifierStart(r) {
				return false
			}
		} else if !isIdentifierPart(r) {
			return false
		}
	}
	return true
}
