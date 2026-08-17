package taxonomy

import "regexp"

// TokenizeRE defines the token boundaries shared by lexical taxonomy ranking
// and the VLM candidate selector.
var TokenizeRE = regexp.MustCompile(`[ ,;:/()\[\]\.]+`)

func tokenize(value string) []string {
	parts := TokenizeRE.Split(value, -1)
	ret := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			ret = append(ret, part)
		}
	}
	return ret
}
