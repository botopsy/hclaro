package state

import (
	"errors"
	"strings"
	"testing"
)

const revealDoc = `{"format_version":"1.0","values":{"root_module":{"resources":[{
 "address":"aws_db_instance.main","mode":"managed","type":"aws_db_instance","name":"main","provider_name":"aws",
 "values":{"name":"main","password":"hunter2","engine_key":"visible","admin":{"pw":"s3cret"},"list":["a","b"],
  "env":[{"name":"DB_PASSWORD","value":"pair-secret"}],"port":5432,"token_n":42,"long":"` + "LONGVALUE" + `",
  "generated":"aB3dE5gH7jK9mN1pQ3sT5"},
 "sensitive_values":{"admin":{"pw":true},"list":[false,true]}}]}}}`

func TestReveal(t *testing.T) {
	in := []byte(revealDoc)
	addr := "aws_db_instance.main"
	ok := []struct {
		path []any
		want any
	}{
		{[]any{"password"}, "hunter2"},                // secret-named
		{[]any{"admin", "pw"}, "s3cret"},              // sensitive-marked
		{[]any{"list", 1.0}, "b"},                     // sensitive list element
		{[]any{"env", 0.0, "value"}, "pair-secret"},   // name/value pair
		{[]any{"generated"}, "aB3dE5gH7jK9mN1pQ3sT5"}, // random-looking
	}
	for _, c := range ok {
		got, err := Reveal(in, addr, c.path)
		if err != nil || got != c.want {
			t.Errorf("%v: got %v, %v; want %v", c.path, got, err, c.want)
		}
	}
	refused := []struct {
		name string
		addr string
		path []any
		want error
	}{
		{"visible", addr, []any{"name"}, ErrRevealNotHidden},
		{"visible list element", addr, []any{"list", 0.0}, ErrRevealNotHidden},
		{"map", addr, []any{"admin"}, ErrRevealNotHidden},
		{"missing path", addr, []any{"nope"}, ErrRevealNotFound},
		{"index out of range", addr, []any{"list", 9.0}, ErrRevealNotFound},
		{"wrong step type", addr, []any{"list", "x"}, ErrRevealNotFound},
		{"unknown resource", "aws_db_instance.other", []any{"password"}, ErrRevealNotFound},
		{"empty path", addr, nil, ErrRevealNotFound},
		{"too deep", addr, make([]any, 65), ErrRevealNotFound},
	}
	for _, c := range refused {
		if got, err := Reveal(in, c.addr, c.path); !errors.Is(err, c.want) || got != nil {
			t.Errorf("%s: got %v, %v; want %v", c.name, got, err, c.want)
		}
	}
}

func TestRevealRefusesContainersAndLongStrings(t *testing.T) {
	doc := func(values string) []byte {
		return []byte(`{"format_version":"1.0","values":{"root_module":{"resources":[{"address":"a.b","mode":"managed","type":"a","name":"b","provider_name":"p","values":` + values + `,"sensitive_values":{"m":true,"s":true}}]}}}`)
	}
	if _, err := Reveal(doc(`{"m":{"k":"v"}}`), "a.b", []any{"m"}); !errors.Is(err, ErrRevealNotScalar) {
		t.Errorf("map: %v", err)
	}
	if _, err := Reveal(doc(`{"s":"`+strings.Repeat("x", maxShownString+1)+`"}`), "a.b", []any{"s"}); !errors.Is(err, ErrRevealTooLong) {
		t.Errorf("long: %v", err)
	}
	if _, err := Reveal([]byte(`{"format_version":"1.0","resource_changes":[]}`), "a.b", []any{"s"}); err == nil {
		t.Error("plan accepted")
	}
}
