package asm

import (
	"testing"

	"encoding/hex"
)

func TestInstructionIteratorValid(t *testing.T) {
	cnt := 0
	script, _ := hex.DecodeString("61000000")

	it := NewInstructionIterator(script)
	for it.Next() {
		cnt++
	}

	if err := it.Error(); err != nil {
		t.Errorf("Expected 2, but encountered error %v instead.", err)
	}
	if cnt != 2 {
		t.Errorf("Expected 2, but got %v instead.", cnt)
	}
}

func TestInstructionIteratorInvalid(t *testing.T) {
	cnt := 0
	script, _ := hex.DecodeString("6100")

	it := NewInstructionIterator(script)
	for it.Next() {
		cnt++
	}

	if it.Error() == nil {
		t.Errorf("Expected an error, but got %v instead.", cnt)
	}
}

func TestInstructionIteratorEmpty(t *testing.T) {
	cnt := 0
	script, _ := hex.DecodeString("")

	it := NewInstructionIterator(script)
	for it.Next() {
		cnt++
	}

	if err := it.Error(); err != nil {
		t.Errorf("Expected 0, but encountered error %v instead.", err)
	}
	if cnt != 0 {
		t.Errorf("Expected 0, but got %v instead.", cnt)
	}
}
