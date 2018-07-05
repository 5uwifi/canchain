//
// (at your option) any later version.
//
//

package core

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"strings"

	"github.com/5uwifi/canchain/accounts/abi"
	"github.com/5uwifi/canchain/common"

	"bytes"
	"os"
	"regexp"
)

type decodedArgument struct {
	soltype abi.Argument
	value   interface{}
}
type decodedCallData struct {
	signature string
	name      string
	inputs    []decodedArgument
}

func (arg decodedArgument) String() string {
	var value string
	switch arg.value.(type) {
	case fmt.Stringer:
		value = arg.value.(fmt.Stringer).String()
	default:
		value = fmt.Sprintf("%v", arg.value)
	}
	return fmt.Sprintf("%v: %v", arg.soltype.Type.String(), value)
}

func (cd decodedCallData) String() string {
	args := make([]string, len(cd.inputs))
	for i, arg := range cd.inputs {
		args[i] = arg.String()
	}
	return fmt.Sprintf("%s(%s)", cd.name, strings.Join(args, ","))
}

func parseCallData(calldata []byte, abidata string) (*decodedCallData, error) {

	if len(calldata) < 4 {
		return nil, fmt.Errorf("Invalid ABI-data, incomplete method signature of (%d bytes)", len(calldata))
	}

	sigdata, argdata := calldata[:4], calldata[4:]
	if len(argdata)%32 != 0 {
		return nil, fmt.Errorf("Not ABI-encoded data; length should be a multiple of 32 (was %d)", len(argdata))
	}

	abispec, err := abi.JSON(strings.NewReader(abidata))
	if err != nil {
		return nil, fmt.Errorf("Failed parsing JSON ABI: %v, abidata: %v", err, abidata)
	}

	method, err := abispec.MethodById(sigdata)
	if err != nil {
		return nil, err
	}

	v, err := method.Inputs.UnpackValues(argdata)
	if err != nil {
		return nil, err
	}

	decoded := decodedCallData{signature: method.Sig(), name: method.Name}

	for n, argument := range method.Inputs {
		if err != nil {
			return nil, fmt.Errorf("Failed to decode argument %d (signature %v): %v", n, method.Sig(), err)
		}
		decodedArg := decodedArgument{
			soltype: argument,
			value:   v[n],
		}
		decoded.inputs = append(decoded.inputs, decodedArg)
	}

	// We're finished decoding the data. At this point, we encode the decoded data to see if it matches with the
	// original data. If we didn't do that, it would e.g. be possible to stuff extra data into the arguments, which
	// is not detected by merely decoding the data.

	var (
		encoded []byte
	)
	encoded, err = method.Inputs.PackValues(v)

	if err != nil {
		return nil, err
	}

	if !bytes.Equal(encoded, argdata) {
		was := common.Bytes2Hex(encoded)
		exp := common.Bytes2Hex(argdata)
		return nil, fmt.Errorf("WARNING: Supplied data is stuffed with extra data. \nWant %s\nHave %s\nfor method %v", exp, was, method.Sig())
	}
	return &decoded, nil
}

func MethodSelectorToAbi(selector string) ([]byte, error) {

	re := regexp.MustCompile(`^([^\)]+)\(([a-z0-9,\[\]]*)\)`)

	type fakeArg struct {
		Type string `json:"type"`
	}
	type fakeABI struct {
		Name   string    `json:"name"`
		Type   string    `json:"type"`
		Inputs []fakeArg `json:"inputs"`
	}
	groups := re.FindStringSubmatch(selector)
	if len(groups) != 3 {
		return nil, fmt.Errorf("Did not match: %v (%v matches)", selector, len(groups))
	}
	name := groups[1]
	args := groups[2]
	arguments := make([]fakeArg, 0)
	if len(args) > 0 {
		for _, arg := range strings.Split(args, ",") {
			arguments = append(arguments, fakeArg{arg})
		}
	}
	abicheat := fakeABI{
		name, "function", arguments,
	}
	return json.Marshal([]fakeABI{abicheat})

}

type AbiDb struct {
	db           map[string]string
	customdb     map[string]string
	customdbPath string
}

func NewEmptyAbiDB() (*AbiDb, error) {
	return &AbiDb{make(map[string]string), make(map[string]string), ""}, nil
}

func NewAbiDBFromFile(path string) (*AbiDb, error) {
	raw, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}
	db, err := NewEmptyAbiDB()
	if err != nil {
		return nil, err
	}
	json.Unmarshal(raw, &db.db)
	return db, nil
}

func NewAbiDBFromFiles(standard, custom string) (*AbiDb, error) {

	db := &AbiDb{make(map[string]string), make(map[string]string), custom}
	db.customdbPath = custom

	raw, err := ioutil.ReadFile(standard)
	if err != nil {
		return nil, err
	}
	json.Unmarshal(raw, &db.db)
	// Custom file may not exist. Will be created during save, if needed
	if _, err := os.Stat(custom); err == nil {
		raw, err = ioutil.ReadFile(custom)
		if err != nil {
			return nil, err
		}
		json.Unmarshal(raw, &db.customdb)
	}

	return db, nil
}

func (db *AbiDb) LookupMethodSelector(id []byte) (string, error) {
	if len(id) < 4 {
		return "", fmt.Errorf("Expected 4-byte id, got %d", len(id))
	}
	sig := common.ToHex(id[:4])
	if key, exists := db.db[sig]; exists {
		return key, nil
	}
	if key, exists := db.customdb[sig]; exists {
		return key, nil
	}
	return "", fmt.Errorf("Signature %v not found", sig)
}
func (db *AbiDb) Size() int {
	return len(db.db)
}

func (db *AbiDb) saveCustomAbi(selector, signature string) error {
	db.customdb[signature] = selector
	if db.customdbPath == "" {
		return nil //Not an error per se, just not used
	}
	d, err := json.Marshal(db.customdb)
	if err != nil {
		return err
	}
	err = ioutil.WriteFile(db.customdbPath, d, 0600)
	return err
}

func (db *AbiDb) AddSignature(selector string, data []byte) error {
	if len(data) < 4 {
		return nil
	}
	_, err := db.LookupMethodSelector(data[:4])
	if err == nil {
		return nil
	}
	sig := common.ToHex(data[:4])
	return db.saveCustomAbi(selector, sig)
}
