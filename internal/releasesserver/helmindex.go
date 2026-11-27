// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package releasesserver

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"

	"go.yaml.in/yaml/v3"
)

// Bounds of a Helm index's read: a line, and the one chart's entries,
// the only part of the index decoded. A YAML decode takes about fifteen
// times its input in memory: 4 MiB of entries stay far under the
// container's limit, where a whole 16 MiB index did not.
const (
	maxIndexLine  = 1 << 20
	maxChartBlock = 4 << 20
)

// errChartBlock is a chart whose entries are past maxChartBlock.
var errChartBlock = fmt.Errorf("the chart's entries are larger than %d MiB", maxChartBlock>>20)

// indexEntry is what is read of a chart version.
type indexEntry struct {
	Version string `json:"version" yaml:"version"`
	Created string `json:"created" yaml:"created"`
}

// chartEntries reads a Helm repository's index (Helm, "The Chart
// Repository Guide") as a stream and decodes only name's entries; found
// says whether the index lists the chart. A JSON index (helm repo index
// --json) is read token by token; a YAML index line by line, in the block
// layout helm repo index writes: a top-level entries mapping, one key per
// chart, its versions indented under it.
func chartEntries(r io.Reader, name string) (out []indexEntry, found bool, err error) {
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		c, err := br.ReadByte()
		if err == io.EOF {
			return nil, false, errors.New("the index is empty")
		}
		if err != nil {
			return nil, false, err
		}
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			continue
		}
		_ = br.UnreadByte()
		if c == '{' {
			return jsonChartEntries(br, name)
		}
		return yamlChartEntries(br, name)
	}
}

// yamlChartEntries scans the index's lines, keeps the asked chart's block
// (its key line and every line under it), dedented, and decodes that
// block alone.
func yamlChartEntries(r io.Reader, name string) ([]indexEntry, bool, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxIndexLine)
	var block bytes.Buffer
	inEntries, capture, child := false, false, -1
	for sc.Scan() {
		line := bytes.TrimRight(sc.Bytes(), " \t\r")
		content := bytes.TrimLeft(line, " ")
		indent := len(line) - len(content)
		if len(content) == 0 || content[0] == '#' {
			continue
		}
		if !inEntries {
			if indent > 0 {
				continue
			}
			key, rest, ok := yamlKey(content)
			if !ok || key != "entries" {
				continue
			}
			if len(rest) > 0 && rest[0] != '#' {
				if bytes.Equal(rest, []byte("{}")) {
					return nil, false, nil
				}
				return nil, false, errors.New("entries is not a block mapping")
			}
			inEntries = true
			continue
		}
		if indent == 0 {
			break
		}
		if child < 0 {
			child = indent
		}
		if indent < child {
			return nil, false, errors.New("entries is not indented evenly")
		}
		if indent == child && content[0] != '-' {
			if capture {
				break
			}
			key, _, ok := yamlKey(content)
			capture = ok && key == name
		}
		if !capture {
			continue
		}
		if block.Len()+len(line)+1-child > maxChartBlock {
			return nil, true, errChartBlock
		}
		block.Write(line[child:])
		block.WriteByte('\n')
	}
	if err := sc.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return nil, false, fmt.Errorf("a line is longer than %d MiB", maxIndexLine>>20)
		}
		return nil, false, err
	}
	if !inEntries {
		return nil, false, errors.New("no entries mapping")
	}
	if !capture {
		return nil, false, nil
	}
	var entries map[string][]indexEntry
	if err := yaml.Unmarshal(block.Bytes(), &entries); err != nil {
		return nil, true, errors.New("the chart's entries do not parse")
	}
	out, ok := entries[name]
	return out, ok, nil
}

// yamlKey splits a block mapping's line into its key, plain or quoted,
// and what follows the colon; ok is false when the line is not a key.
func yamlKey(line []byte) (key string, rest []byte, ok bool) {
	var end int
	switch line[0] {
	case '"':
		i := 1
		for ; i < len(line) && line[i] != '"'; i++ {
			if line[i] == '\\' {
				i++
			}
		}
		if i >= len(line) {
			return "", nil, false
		}
		k, err := strconv.Unquote(string(line[:i+1]))
		if err != nil {
			return "", nil, false
		}
		key, end = k, i+1
	case '\'':
		i := 1
		for ; i < len(line); i++ {
			if line[i] == '\'' {
				if i+1 < len(line) && line[i+1] == '\'' {
					i++
					continue
				}
				break
			}
		}
		if i >= len(line) {
			return "", nil, false
		}
		key, end = string(bytes.ReplaceAll(line[1:i], []byte("''"), []byte("'"))), i+1
	default:
		i := bytes.Index(line, []byte(": "))
		if i < 0 {
			if line[len(line)-1] != ':' {
				return "", nil, false
			}
			i = len(line) - 1
		}
		key, end = string(line[:i]), i
	}
	if end >= len(line) || line[end] != ':' {
		return "", nil, false
	}
	return key, bytes.TrimSpace(line[end+1:]), true
}

// jsonChartEntries reads a JSON index's tokens, skipping every value but
// entries' name.
func jsonChartEntries(r io.Reader, name string) ([]indexEntry, bool, error) {
	dec := json.NewDecoder(r)
	if err := expectDelim(dec, '{'); err != nil {
		return nil, false, err
	}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return nil, false, err
		}
		if key != "entries" {
			if err := skipValue(dec); err != nil {
				return nil, false, err
			}
			continue
		}
		if err := expectDelim(dec, '{'); err != nil {
			return nil, false, err
		}
		for dec.More() {
			chart, err := dec.Token()
			if err != nil {
				return nil, false, err
			}
			if chart != name {
				if err := skipValue(dec); err != nil {
					return nil, false, err
				}
				continue
			}
			var out []indexEntry
			if err := dec.Decode(&out); err != nil {
				return nil, true, errors.New("the chart's entries do not parse")
			}
			return out, true, nil
		}
		return nil, false, nil
	}
	return nil, false, errors.New("no entries mapping")
}

func expectDelim(dec *json.Decoder, d json.Delim) error {
	t, err := dec.Token()
	if err != nil {
		return err
	}
	if t != d {
		return fmt.Errorf("want %v, got %v", d, t)
	}
	return nil
}

// skipValue reads one value's tokens without holding them.
func skipValue(dec *json.Decoder) error {
	depth := 0
	for {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		switch t {
		case json.Delim('{'), json.Delim('['):
			depth++
		case json.Delim('}'), json.Delim(']'):
			depth--
		}
		if depth == 0 {
			return nil
		}
	}
}
