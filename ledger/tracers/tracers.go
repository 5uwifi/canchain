package tracers

import (
	"strings"
	"unicode"

	"github.com/5uwifi/canchain/ledger/tracers/internal/tracers"
)

var all = make(map[string]string)

func camel(str string) string {
	pieces := strings.Split(str, "_")
	for i := 1; i < len(pieces); i++ {
		pieces[i] = string(unicode.ToUpper(rune(pieces[i][0]))) + pieces[i][1:]
	}
	return strings.Join(pieces, "")
}

func init() {
	for _, file := range tracers.AssetNames() {
		name := camel(strings.TrimSuffix(file, ".js"))
		all[name] = string(tracers.MustAsset(file))
	}
}

func tracer(name string) (string, bool) {
	if tracer, ok := all[name]; ok {
		return tracer, true
	}
	return "", false
}
