package cli

import (
	"flag"
	"io"
	"testing"
)

func TestArguments(t *testing.T) {
	o, err := Parse([]string{"--path", "./prod", "--port", "8080", "--engine", "tofu", "--no-browser"}, io.Discard)
	if err != nil || o.Path != "./prod" || o.Port != 8080 || o.Engine != "tofu" || !o.NoBrowser {
		t.Fatalf("%+v %v", o, err)
	}
	for _, args := range [][]string{{"--port", "-1"}, {"--port", "65536"}, {"--engine", "sh"}, {"extra"}, {"--wat"}} {
		if _, err := Parse(args, io.Discard); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	if _, err := Parse([]string{"--help"}, io.Discard); err != flag.ErrHelp {
		t.Fatal(err)
	}
	if o, err := Parse([]string{"--version"}, io.Discard); err != nil || !o.Version {
		t.Fatal(err)
	}
}
