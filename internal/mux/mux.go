// Package mux defines the per-stream framing carried over the QUIC connection.
//
// Each logical operation is a single QUIC stream. The opener first writes a
// length-prefixed JSON OpenMsg describing the stream's purpose; the acceptor
// reads it and dispatches. After the header, bytes are the operation's payload.
package mux

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
)

// Kind enumerates stream purposes.
type Kind string

const (
	// KindTCP: tunnel a TCP connection. Target is "host:port" the acceptor
	// should dial. Used for both fixed forwards (-L) and SOCKS (-D).
	KindTCP Kind = "tcp"
	// KindFile: transfer a file. See FileHeader.
	KindFile Kind = "file"
	// KindExec: run a command on the acceptor. See ExecHeader.
	KindExec Kind = "exec"
)

// OpenMsg is the first frame on every stream.
type OpenMsg struct {
	Kind   Kind        `json:"kind"`
	Target string      `json:"target,omitempty"` // KindTCP dial target
	File   *FileHeader `json:"file,omitempty"`
	Exec   *ExecHeader `json:"exec,omitempty"`
}

// FileHeader describes a file transfer. Op is "put" (opener -> acceptor writes
// to disk) or "get" (acceptor reads from disk -> opener).
type FileHeader struct {
	Op   string `json:"op"`   // "put" | "get"
	Path string `json:"path"` // path on the acceptor (remote) side
	Size int64  `json:"size"` // for put: bytes that follow
	Mode uint32 `json:"mode"`
}

// ExecHeader describes a command to run on the acceptor.
type ExecHeader struct {
	Args []string `json:"args"`
	PTY  bool     `json:"pty,omitempty"` // reserved; not implemented on Windows host
}

const maxHeader = 64 * 1024

// WriteHeader writes a length-prefixed JSON OpenMsg.
func WriteHeader(w io.Writer, m OpenMsg) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if len(b) > maxHeader {
		return errors.New("header too large")
	}
	var lp [4]byte
	binary.BigEndian.PutUint32(lp[:], uint32(len(b)))
	if _, err := w.Write(lp[:]); err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// ReadHeader reads a length-prefixed JSON OpenMsg.
func ReadHeader(r io.Reader) (OpenMsg, error) {
	var lp [4]byte
	if _, err := io.ReadFull(r, lp[:]); err != nil {
		return OpenMsg{}, err
	}
	n := binary.BigEndian.Uint32(lp[:])
	if n > maxHeader {
		return OpenMsg{}, errors.New("header too large")
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return OpenMsg{}, err
	}
	var m OpenMsg
	if err := json.Unmarshal(buf, &m); err != nil {
		return OpenMsg{}, err
	}
	return m, nil
}
