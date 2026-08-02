package painter

import (
	"errors"
	"testing"
)

func TestExpandSubstitutes(t *testing.T) {
	got, err := Expand("{{A}}-{{B}}", map[string]string{"A": "x", "B": "y"})
	if err != nil || got != "x-y" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestExpandUndefinedVarFailsLoud(t *testing.T) {
	_, err := Expand("{{NOPE}}", map[string]string{})
	if !errors.Is(err, ErrUndefinedVar) {
		t.Fatalf("want ErrUndefinedVar, got %v", err)
	}
}

func TestExpandNeverRescansSubstitution(t *testing.T) {
	got, err := Expand("{{A}}", map[string]string{"A": "{{B}}", "B": "boom"})
	if err != nil || got != "{{B}}" {
		t.Fatalf("substituted text must not re-expand: got %q err %v", got, err)
	}
}

func TestExpandUnterminatedIsLiteral(t *testing.T) {
	got, err := Expand("tail {{OPEN", map[string]string{})
	if err != nil || got != "tail {{OPEN" {
		t.Fatalf("got %q err %v", got, err)
	}
}
