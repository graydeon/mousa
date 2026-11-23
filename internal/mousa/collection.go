package mousa

type Collection struct {
	Name    string     `json:"name"`
	Sources []SourceID `json:"sources"`
}

func ValidCollectionName(name string) bool {
	if len(name) == 0 || len(name) > 64 {
		return false
	}
	for i, c := range name {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			(i > 0 && (c == '-' || c == '_' || c == '.')) {
			continue
		}
		return false
	}
	return true
}
