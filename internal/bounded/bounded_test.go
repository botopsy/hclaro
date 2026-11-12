package bounded

import "testing"

func TestBufferKeepsTheFirstLimitBytes(t *testing.T) {
	b := &Buffer{Limit: 5}
	for _, chunk := range []string{"abc", "", "de"} {
		if n, err := b.Write([]byte(chunk)); n != len(chunk) || err != nil {
			t.Fatalf("Write(%q) = %d, %v", chunk, n, err)
		}
	}
	if b.String() != "abcde" || b.Overflow {
		t.Fatalf("at the limit: %q overflow=%v", b.String(), b.Overflow)
	}
	// Past the limit the writer is still told everything was written, so the pipe drains.
	if n, err := b.Write([]byte("fgh")); n != 3 || err != nil || b.String() != "abcde" || !b.Overflow {
		t.Fatalf("past the limit: %d %v %q overflow=%v", n, err, b.String(), b.Overflow)
	}
	split := &Buffer{Limit: 4}
	split.Write([]byte("abcdef"))
	if string(split.Bytes()) != "abcd" || split.Len() != 4 || !split.Overflow {
		t.Fatalf("split write: %q overflow=%v", split.Bytes(), split.Overflow)
	}
}
