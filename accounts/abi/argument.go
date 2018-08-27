package abi

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

type Argument struct {
	Name    string
	Type    Type
	Indexed bool
}

type Arguments []Argument

func (argument *Argument) UnmarshalJSON(data []byte) error {
	var extarg struct {
		Name    string
		Type    string
		Indexed bool
	}
	err := json.Unmarshal(data, &extarg)
	if err != nil {
		return fmt.Errorf("argument json err: %v", err)
	}

	argument.Type, err = NewType(extarg.Type)
	if err != nil {
		return err
	}
	argument.Name = extarg.Name
	argument.Indexed = extarg.Indexed

	return nil
}

func (arguments Arguments) LengthNonIndexed() int {
	out := 0
	for _, arg := range arguments {
		if !arg.Indexed {
			out++
		}
	}
	return out
}

func (arguments Arguments) NonIndexed() Arguments {
	var ret []Argument
	for _, arg := range arguments {
		if !arg.Indexed {
			ret = append(ret, arg)
		}
	}
	return ret
}

func (arguments Arguments) isTuple() bool {
	return len(arguments) > 1
}

func (arguments Arguments) Unpack(v interface{}, data []byte) error {

	if reflect.Ptr != reflect.ValueOf(v).Kind() {
		return fmt.Errorf("abi: Unpack(non-pointer %T)", v)
	}
	marshalledValues, err := arguments.UnpackValues(data)
	if err != nil {
		return err
	}
	if arguments.isTuple() {
		return arguments.unpackTuple(v, marshalledValues)
	}
	return arguments.unpackAtomic(v, marshalledValues)
}

func (arguments Arguments) unpackTuple(v interface{}, marshalledValues []interface{}) error {

	var (
		value = reflect.ValueOf(v).Elem()
		typ   = value.Type()
		kind  = value.Kind()
	)

	if err := requireUnpackKind(value, typ, kind, arguments); err != nil {
		return err
	}


	var abi2struct map[string]string
	if kind == reflect.Struct {
		var err error
		abi2struct, err = mapAbiToStructFields(arguments, value)
		if err != nil {
			return err
		}
	}
	for i, arg := range arguments.NonIndexed() {

		reflectValue := reflect.ValueOf(marshalledValues[i])

		switch kind {
		case reflect.Struct:
			if structField, ok := abi2struct[arg.Name]; ok {
				if err := set(value.FieldByName(structField), reflectValue, arg); err != nil {
					return err
				}
			}
		case reflect.Slice, reflect.Array:
			if value.Len() < i {
				return fmt.Errorf("abi: insufficient number of arguments for unpack, want %d, got %d", len(arguments), value.Len())
			}
			v := value.Index(i)
			if err := requireAssignable(v, reflectValue); err != nil {
				return err
			}

			if err := set(v.Elem(), reflectValue, arg); err != nil {
				return err
			}
		default:
			return fmt.Errorf("abi:[2] cannot unmarshal tuple in to %v", typ)
		}
	}
	return nil
}

func (arguments Arguments) unpackAtomic(v interface{}, marshalledValues []interface{}) error {
	if len(marshalledValues) != 1 {
		return fmt.Errorf("abi: wrong length, expected single value, got %d", len(marshalledValues))
	}

	elem := reflect.ValueOf(v).Elem()
	kind := elem.Kind()
	reflectValue := reflect.ValueOf(marshalledValues[0])

	var abi2struct map[string]string
	if kind == reflect.Struct {
		var err error
		if abi2struct, err = mapAbiToStructFields(arguments, elem); err != nil {
			return err
		}
		arg := arguments.NonIndexed()[0]
		if structField, ok := abi2struct[arg.Name]; ok {
			return set(elem.FieldByName(structField), reflectValue, arg)
		}
		return nil
	}

	return set(elem, reflectValue, arguments.NonIndexed()[0])

}

func getArraySize(arr *Type) int {
	size := arr.Size
	arr = arr.Elem
	for arr.T == ArrayTy {
		size *= arr.Size
		arr = arr.Elem
	}
	return size
}

func (arguments Arguments) UnpackValues(data []byte) ([]interface{}, error) {
	retval := make([]interface{}, 0, arguments.LengthNonIndexed())
	virtualArgs := 0
	for index, arg := range arguments.NonIndexed() {
		marshalledValue, err := toGoType((index+virtualArgs)*32, arg.Type, data)
		if arg.Type.T == ArrayTy {
			virtualArgs += getArraySize(&arg.Type) - 1
		}
		if err != nil {
			return nil, err
		}
		retval = append(retval, marshalledValue)
	}
	return retval, nil
}

func (arguments Arguments) PackValues(args []interface{}) ([]byte, error) {
	return arguments.Pack(args...)
}

func (arguments Arguments) Pack(args ...interface{}) ([]byte, error) {
	abiArgs := arguments
	if len(args) != len(abiArgs) {
		return nil, fmt.Errorf("argument count mismatch: %d for %d", len(args), len(abiArgs))
	}
	var variableInput []byte

	inputOffset := 0
	for _, abiArg := range abiArgs {
		if abiArg.Type.T == ArrayTy {
			inputOffset += 32 * abiArg.Type.Size
		} else {
			inputOffset += 32
		}
	}
	var ret []byte
	for i, a := range args {
		input := abiArgs[i]
		packed, err := input.Type.pack(reflect.ValueOf(a))
		if err != nil {
			return nil, err
		}
		if input.Type.requiresLengthPrefix() {
			offset := inputOffset + len(variableInput)
			ret = append(ret, packNum(reflect.ValueOf(offset))...)
			variableInput = append(variableInput, packed...)
		} else {
			ret = append(ret, packed...)
		}
	}
	ret = append(ret, variableInput...)

	return ret, nil
}

func capitalise(input string) string {
	for len(input) > 0 && input[0] == '_' {
		input = input[1:]
	}
	if len(input) == 0 {
		return ""
	}
	return strings.ToUpper(input[:1]) + input[1:]
}
