// Package comfy holds pure-Go ComfyUI metadata parsing: extracting the embedded
// workflow graphs from a PNG's tEXt chunks and classifying/inspecting the graph
// JSON. Slice A does NO network I/O — it only reads and understands workflow
// bytes that come from untrusted uploads, so every parser here is defensive
// (bounded reads, no panics on malformed input).
package comfy

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
)

// Sentinel errors for PNG extraction.
var (
	// ErrNoWorkflow means the PNG carried neither a ComfyUI `prompt` (API graph)
	// nor a `workflow` (UI graph) tEXt chunk.
	ErrNoWorkflow = errors.New("no comfy workflow metadata in PNG")
	// ErrA1111Only means the PNG carried only an A1111 `parameters` string (a flat
	// non-JSON generation blob), not a ComfyUI graph.
	ErrA1111Only = errors.New("PNG contains A1111 parameters, not a comfy workflow")
	// ErrInvalidPNG means the input did not parse as a PNG (bad signature,
	// truncated chunk, or oversized input).
	ErrInvalidPNG = errors.New("invalid or truncated PNG")
)

// maxPNGBytes caps how many bytes ExtractFromPNG will read from an untrusted
// upload. A ComfyUI PNG's metadata lives in early tEXt chunks, but a real image
// can be large; 64 MiB is a generous ceiling that still refuses a
// resource-exhaustion payload.
const maxPNGBytes = 64 << 20

// pngSignature is the fixed 8-byte PNG magic.
var pngSignature = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

// Extracted is the result of reading a PNG's ComfyUI metadata.
type Extracted struct {
	// APIGraph is the api-format graph from the `prompt` tEXt chunk (directly
	// submittable to ComfyUI). Nil when absent.
	APIGraph json.RawMessage
	// UIGraph is the editor graph from the `workflow` tEXt chunk. Nil when absent.
	UIGraph json.RawMessage
	// IsA1111 is true when the PNG carried an A1111 `parameters` chunk (and no
	// comfy graph) — surfaced so callers can give a clear "not a comfy workflow"
	// message.
	IsA1111 bool
}

// ExtractFromPNG walks the PNG chunk stream in r and returns the embedded
// ComfyUI graphs. It reads the api graph from the tEXt keyword `prompt` and the
// ui graph from the tEXt keyword `workflow`. It returns ErrNoWorkflow when
// neither is present, ErrA1111Only when only an A1111 `parameters` chunk is
// present, and ErrInvalidPNG for a malformed/truncated/oversized stream.
func ExtractFromPNG(r io.Reader) (Extracted, error) {
	// Bound the read so an untrusted upload cannot exhaust memory. The +1 lets us
	// detect an over-limit stream rather than silently truncating a huge file into
	// a "valid-looking" prefix.
	br := bufio.NewReader(io.LimitReader(r, maxPNGBytes+1))

	sig := make([]byte, 8)
	if _, err := io.ReadFull(br, sig); err != nil {
		return Extracted{}, ErrInvalidPNG
	}
	if !bytes.Equal(sig, pngSignature) {
		return Extracted{}, ErrInvalidPNG
	}

	var (
		out   Extracted
		read  = int64(8)
		texts = map[string]string{}
	)
	for {
		var length uint32
		if err := binary.Read(br, binary.BigEndian, &length); err != nil {
			if err == io.EOF {
				// Ran out of chunks without hitting IEND: tolerate it and use
				// whatever tEXt chunks we already collected.
				break
			}
			return Extracted{}, ErrInvalidPNG
		}
		// A single chunk's declared length must be sane. PNG chunk lengths are
		// spec-capped at 2^31-1; reject anything absurd up front.
		if length > 0x7fffffff {
			return Extracted{}, ErrInvalidPNG
		}
		read += 4 + int64(length) + 8 // length + type + data + crc
		if read > maxPNGBytes {
			return Extracted{}, ErrInvalidPNG
		}

		typ := make([]byte, 4)
		if _, err := io.ReadFull(br, typ); err != nil {
			return Extracted{}, ErrInvalidPNG
		}
		data := make([]byte, length)
		if _, err := io.ReadFull(br, data); err != nil {
			return Extracted{}, ErrInvalidPNG
		}
		crc := make([]byte, 4)
		if _, err := io.ReadFull(br, crc); err != nil {
			return Extracted{}, ErrInvalidPNG
		}

		switch string(typ) {
		case "tEXt":
			if k, v, ok := parseTEXt(data); ok {
				// First occurrence of a keyword wins (ComfyUI writes each once).
				if _, exists := texts[k]; !exists {
					texts[k] = v
				}
			}
		case "IEND":
			// End of the datastream; stop even if the reader has trailing bytes.
			goto done
		}
	}
done:

	if v, ok := texts["prompt"]; ok && looksLikeJSON(v) {
		out.APIGraph = json.RawMessage(v)
	}
	if v, ok := texts["workflow"]; ok && looksLikeJSON(v) {
		out.UIGraph = json.RawMessage(v)
	}
	if out.APIGraph != nil || out.UIGraph != nil {
		return out, nil
	}
	if _, ok := texts["parameters"]; ok {
		out.IsA1111 = true
		return out, ErrA1111Only
	}
	return out, ErrNoWorkflow
}

// parseTEXt splits a tEXt chunk payload into its (keyword, value) pair. The PNG
// spec encodes it as keyword, a single 0x00 separator, then the latin-1 text
// (which may itself contain no further constraint). Returns ok=false when the
// separator is missing.
func parseTEXt(data []byte) (keyword, value string, ok bool) {
	i := bytes.IndexByte(data, 0)
	if i < 0 {
		return "", "", false
	}
	return string(data[:i]), string(data[i+1:]), true
}

// looksLikeJSON reports whether s begins with a JSON object or array (after
// leading whitespace).
//
// 🔴 IT IS A PRE-FILTER, NOT A VALIDATOR, AND IT NEVER CLASSIFIES. It inspects one
// byte: a truncated, wrapped or UI-shaped value under the `prompt` keyword passes
// it just as readily as a real api graph. Deciding what a chunk actually IS is the
// caller's job, through comfy.DetectFormat — handleWorkflowImportPNG used to skip
// that step and store a `prompt` chunk as format=api on this check alone.
//
// It still earns its place, for a reason a stricter check would break: it is what
// keeps ErrA1111Only and ErrNoWorkflow REACHABLE. A PNG carrying a junk `prompt`
// value alongside a real `parameters` chunk must report "A1111 parameters, not a
// comfy workflow"; without this filter APIGraph would be non-nil and that branch
// could never fire, costing the user the accurate message.
func looksLikeJSON(s string) bool {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ' ', '\t', '\r', '\n':
			continue
		case '{', '[':
			return true
		default:
			return false
		}
	}
	return false
}
