// Package bounded holds the output buffer that the engine graph, Git, AWS CLI and Infracost
// commands write into.
package bounded

import "bytes"

// Buffer keeps the first Limit bytes written to it and sets Overflow when more arrive. Write
// never fails and always reports the full length, so a child process keeps draining its pipe
// instead of blocking.
type Buffer struct {
	Limit    int
	Overflow bool
	buf      bytes.Buffer
}

func (b *Buffer) Write(p []byte) (int, error) {
	n := len(p)
	if room := b.Limit - b.buf.Len(); room < n {
		b.Overflow = true
		p = p[:max(room, 0)]
	}
	b.buf.Write(p)
	return n, nil
}

func (b *Buffer) Bytes() []byte  { return b.buf.Bytes() }
func (b *Buffer) String() string { return b.buf.String() }
func (b *Buffer) Len() int       { return b.buf.Len() }
