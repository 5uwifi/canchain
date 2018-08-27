package bind

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"text/template"
	"unicode"

	"github.com/5uwifi/canchain/accounts/abi"
	"golang.org/x/tools/imports"
)

type Lang int

const (
	LangGo Lang = iota
	LangJava
	LangObjC
)

func Bind(types []string, abis []string, bytecodes []string, pkg string, lang Lang) (string, error) {
	contracts := make(map[string]*tmplContract)

	for i := 0; i < len(types); i++ {
		evmABI, err := abi.JSON(strings.NewReader(abis[i]))
		if err != nil {
			return "", err
		}
		strippedABI := strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) {
				return -1
			}
			return r
		}, abis[i])

		var (
			calls     = make(map[string]*tmplMethod)
			transacts = make(map[string]*tmplMethod)
			events    = make(map[string]*tmplEvent)
		)
		for _, original := range evmABI.Methods {
			normalized := original
			normalized.Name = methodNormalizer[lang](original.Name)

			normalized.Inputs = make([]abi.Argument, len(original.Inputs))
			copy(normalized.Inputs, original.Inputs)
			for j, input := range normalized.Inputs {
				if input.Name == "" {
					normalized.Inputs[j].Name = fmt.Sprintf("arg%d", j)
				}
			}
			normalized.Outputs = make([]abi.Argument, len(original.Outputs))
			copy(normalized.Outputs, original.Outputs)
			for j, output := range normalized.Outputs {
				if output.Name != "" {
					normalized.Outputs[j].Name = capitalise(output.Name)
				}
			}
			if original.Const {
				calls[original.Name] = &tmplMethod{Original: original, Normalized: normalized, Structured: structured(original.Outputs)}
			} else {
				transacts[original.Name] = &tmplMethod{Original: original, Normalized: normalized, Structured: structured(original.Outputs)}
			}
		}
		for _, original := range evmABI.Events {
			if original.Anonymous {
				continue
			}
			normalized := original
			normalized.Name = methodNormalizer[lang](original.Name)

			normalized.Inputs = make([]abi.Argument, len(original.Inputs))
			copy(normalized.Inputs, original.Inputs)
			for j, input := range normalized.Inputs {
				if input.Indexed {
					if input.Name == "" {
						normalized.Inputs[j].Name = fmt.Sprintf("arg%d", j)
					}
				}
			}
			events[original.Name] = &tmplEvent{Original: original, Normalized: normalized}
		}
		contracts[types[i]] = &tmplContract{
			Type:        capitalise(types[i]),
			InputABI:    strings.Replace(strippedABI, "\"", "\\\"", -1),
			InputBin:    strings.TrimSpace(bytecodes[i]),
			Constructor: evmABI.Constructor,
			Calls:       calls,
			Transacts:   transacts,
			Events:      events,
		}
	}
	data := &tmplData{
		Package:   pkg,
		Contracts: contracts,
	}
	buffer := new(bytes.Buffer)

	funcs := map[string]interface{}{
		"bindtype":      bindType[lang],
		"bindtopictype": bindTopicType[lang],
		"namedtype":     namedType[lang],
		"capitalise":    capitalise,
		"decapitalise":  decapitalise,
	}
	tmpl := template.Must(template.New("").Funcs(funcs).Parse(tmplSource[lang]))
	if err := tmpl.Execute(buffer, data); err != nil {
		return "", err
	}
	if lang == LangGo {
		code, err := imports.Process(".", buffer.Bytes(), nil)
		if err != nil {
			return "", fmt.Errorf("%v\n%s", err, buffer)
		}
		return string(code), nil
	}
	return buffer.String(), nil
}

var bindType = map[Lang]func(kind abi.Type) string{
	LangGo:   bindTypeGo,
	LangJava: bindTypeJava,
}

func wrapArray(stringKind string, innerLen int, innerMapping string) (string, []string) {
	remainder := stringKind[innerLen:]
	matches := regexp.MustCompile(`\[(\d*)\]`).FindAllStringSubmatch(remainder, -1)
	parts := make([]string, 0, len(matches))
	for _, match := range matches {
		parts = append(parts, match[1])
	}
	return innerMapping, parts
}

func arrayBindingGo(inner string, arraySizes []string) string {
	out := ""
	for i := len(arraySizes) - 1; i >= 0; i-- {
		out += "[" + arraySizes[i] + "]"
	}
	out += inner
	return out
}

func bindTypeGo(kind abi.Type) string {
	stringKind := kind.String()
	innerLen, innerMapping := bindUnnestedTypeGo(stringKind)
	return arrayBindingGo(wrapArray(stringKind, innerLen, innerMapping))
}

func bindUnnestedTypeGo(stringKind string) (int, string) {

	switch {
	case strings.HasPrefix(stringKind, "address"):
		return len("address"), "common.Address"

	case strings.HasPrefix(stringKind, "bytes"):
		parts := regexp.MustCompile(`bytes([0-9]*)`).FindStringSubmatch(stringKind)
		return len(parts[0]), fmt.Sprintf("[%s]byte", parts[1])

	case strings.HasPrefix(stringKind, "int") || strings.HasPrefix(stringKind, "uint"):
		parts := regexp.MustCompile(`(u)?int([0-9]*)`).FindStringSubmatch(stringKind)
		switch parts[2] {
		case "8", "16", "32", "64":
			return len(parts[0]), fmt.Sprintf("%sint%s", parts[1], parts[2])
		}
		return len(parts[0]), "*big.Int"

	case strings.HasPrefix(stringKind, "bool"):
		return len("bool"), "bool"

	case strings.HasPrefix(stringKind, "string"):
		return len("string"), "string"

	default:
		return len(stringKind), stringKind
	}
}

func arrayBindingJava(inner string, arraySizes []string) string {
	return inner + strings.Repeat("[]", len(arraySizes))
}

func bindTypeJava(kind abi.Type) string {
	stringKind := kind.String()
	innerLen, innerMapping := bindUnnestedTypeJava(stringKind)
	return arrayBindingJava(wrapArray(stringKind, innerLen, innerMapping))
}

func bindUnnestedTypeJava(stringKind string) (int, string) {

	switch {
	case strings.HasPrefix(stringKind, "address"):
		parts := regexp.MustCompile(`address(\[[0-9]*\])?`).FindStringSubmatch(stringKind)
		if len(parts) != 2 {
			return len(stringKind), stringKind
		}
		if parts[1] == "" {
			return len("address"), "Address"
		}
		return len(parts[0]), "Addresses"

	case strings.HasPrefix(stringKind, "bytes"):
		parts := regexp.MustCompile(`bytes([0-9]*)`).FindStringSubmatch(stringKind)
		if len(parts) != 2 {
			return len(stringKind), stringKind
		}
		return len(parts[0]), "byte[]"

	case strings.HasPrefix(stringKind, "int") || strings.HasPrefix(stringKind, "uint"):
		parts := regexp.MustCompile(`(u)?int([0-9]*)`).FindStringSubmatch(stringKind)
		if len(parts) != 3 {
			return len(stringKind), stringKind
		}

		namedSize := map[string]string{
			"8":  "byte",
			"16": "short",
			"32": "int",
			"64": "long",
		}[parts[2]]

		if namedSize == "" {
			namedSize = "BigInt"
		}
		return len(parts[0]), namedSize

	case strings.HasPrefix(stringKind, "bool"):
		return len("bool"), "boolean"

	case strings.HasPrefix(stringKind, "string"):
		return len("string"), "String"

	default:
		return len(stringKind), stringKind
	}
}

var bindTopicType = map[Lang]func(kind abi.Type) string{
	LangGo:   bindTopicTypeGo,
	LangJava: bindTopicTypeJava,
}

func bindTopicTypeGo(kind abi.Type) string {
	bound := bindTypeGo(kind)
	if bound == "string" || bound == "[]byte" {
		bound = "common.Hash"
	}
	return bound
}

func bindTopicTypeJava(kind abi.Type) string {
	bound := bindTypeJava(kind)
	if bound == "String" || bound == "Bytes" {
		bound = "Hash"
	}
	return bound
}

var namedType = map[Lang]func(string, abi.Type) string{
	LangGo:   func(string, abi.Type) string { panic("this shouldn't be needed") },
	LangJava: namedTypeJava,
}

func namedTypeJava(javaKind string, solKind abi.Type) string {
	switch javaKind {
	case "byte[]":
		return "Binary"
	case "byte[][]":
		return "Binaries"
	case "string":
		return "String"
	case "string[]":
		return "Strings"
	case "boolean":
		return "Bool"
	case "boolean[]":
		return "Bools"
	case "BigInt[]":
		return "BigInts"
	default:
		parts := regexp.MustCompile(`(u)?int([0-9]*)(\[[0-9]*\])?`).FindStringSubmatch(solKind.String())
		if len(parts) != 4 {
			return javaKind
		}
		switch parts[2] {
		case "8", "16", "32", "64":
			if parts[3] == "" {
				return capitalise(fmt.Sprintf("%sint%s", parts[1], parts[2]))
			}
			return capitalise(fmt.Sprintf("%sint%ss", parts[1], parts[2]))

		default:
			return javaKind
		}
	}
}

var methodNormalizer = map[Lang]func(string) string{
	LangGo:   capitalise,
	LangJava: decapitalise,
}

func capitalise(input string) string {
	for len(input) > 0 && input[0] == '_' {
		input = input[1:]
	}
	if len(input) == 0 {
		return ""
	}
	return toCamelCase(strings.ToUpper(input[:1]) + input[1:])
}

func decapitalise(input string) string {
	for len(input) > 0 && input[0] == '_' {
		input = input[1:]
	}
	if len(input) == 0 {
		return ""
	}
	return toCamelCase(strings.ToLower(input[:1]) + input[1:])
}

func toCamelCase(input string) string {
	toupper := false

	result := ""
	for k, v := range input {
		switch {
		case k == 0:
			result = strings.ToUpper(string(input[0]))

		case toupper:
			result += strings.ToUpper(string(v))
			toupper = false

		case v == '_':
			toupper = true

		default:
			result += string(v)
		}
	}
	return result
}

func structured(args abi.Arguments) bool {
	if len(args) < 2 {
		return false
	}
	exists := make(map[string]bool)
	for _, out := range args {
		if out.Name == "" {
			return false
		}
		field := capitalise(out.Name)
		if field == "" || exists[field] {
			return false
		}
		exists[field] = true
	}
	return true
}
