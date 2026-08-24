package main

import (
	"bytes"
	"io"
	"sync"
)

type LogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *LogBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.buf.Len() > 1024*1024 {
		current := b.buf.Bytes()
		keep := append([]byte(nil), current[len(current)/2:]...)
		b.buf.Reset()
		_, _ = b.buf.Write(keep)
	}
	return b.buf.Write(data)
}

func (b *LogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func multiWriter(file io.Writer, buffer *LogBuffer) io.Writer {
	return io.MultiWriter(file, buffer)
}
