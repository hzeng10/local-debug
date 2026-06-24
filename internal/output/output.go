// Package output renders command results for two audiences: a human reading a
// terminal, and ClaudeCode (or any agent/script) reading --json. Every command
// funnels through a Printer so the JSON envelope shape is uniform and secret
// values are masked consistently.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Format selects how results are rendered.
type Format string

const (
	Human Format = "human"
	JSON  Format = "json"
)

// Printer writes command output in the selected format.
type Printer struct {
	Format Format
	Out    io.Writer
	Err    io.Writer
}

// New returns a Printer; jsonOut=true selects machine-readable JSON.
func New(out, err io.Writer, jsonOut bool) *Printer {
	f := Human
	if jsonOut {
		f = JSON
	}
	return &Printer{Format: f, Out: out, Err: err}
}

// Envelope is the stable top-level shape every --json response uses, so an agent
// can branch on ok/error without knowing each command's payload schema.
type Envelope struct {
	OK      bool        `json:"ok"`
	Command string      `json:"command"`
	Data    interface{} `json:"data,omitempty"`
	Error   string      `json:"error,omitempty"`
	Hint    string      `json:"hint,omitempty"`
}

// Result prints a success. In human mode it writes humanText; in JSON mode it
// wraps data in an Envelope. data should be a struct with json tags.
func (p *Printer) Result(command, humanText string, data interface{}) {
	if p.Format == JSON {
		p.writeJSON(Envelope{OK: true, Command: command, Data: data})
		return
	}
	if humanText != "" {
		fmt.Fprintln(p.Out, humanText)
	}
}

// Failf prints an error consistently and returns it so callers can also use it
// as the command's returned error (which drives a non-zero exit code).
func (p *Printer) Failf(command, hint string, err error) error {
	if p.Format == JSON {
		p.writeJSON(Envelope{OK: false, Command: command, Error: err.Error(), Hint: hint})
		return err
	}
	fmt.Fprintf(p.Err, "error: %v\n", err)
	if hint != "" {
		fmt.Fprintf(p.Err, "hint: %s\n", hint)
	}
	return err
}

// Info writes a human-only progress line (suppressed in JSON mode to keep stdout
// a single valid JSON document).
func (p *Printer) Info(format string, a ...interface{}) {
	if p.Format == JSON {
		return
	}
	fmt.Fprintf(p.Out, format+"\n", a...)
}

func (p *Printer) writeJSON(e Envelope) {
	enc := json.NewEncoder(p.Out)
	enc.SetIndent("", "  ")
	_ = enc.Encode(e)
}

// Mask redacts a secret value for display, keeping a short prefix so a human can
// still correlate it without exposing the secret. Empty stays empty.
func Mask(v string) string {
	if v == "" {
		return ""
	}
	if len(v) <= 4 {
		return "****"
	}
	return v[:2] + strings.Repeat("*", len(v)-2)
}
