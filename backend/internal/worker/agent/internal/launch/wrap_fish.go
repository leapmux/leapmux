package launch

import "strings"

func fishClearEnv(keys []string) string {
	var body strings.Builder
	for _, key := range keys {
		// An unexported global shadows a universal without changing its saved value.
		body.WriteString("set --global --unexport " + key + "; or exit $status; ")
	}
	return body.String()
}

func fishSetEnv(pins []environmentPin) string {
	var body strings.Builder
	for _, pin := range pins {
		body.WriteString("set --global --export " + pin.target + " \"$" + pin.source + "\"; or exit $status; ")
		body.WriteString("set --global --unexport " + pin.source + "; or exit $status; ")
	}
	return body.String()
}
