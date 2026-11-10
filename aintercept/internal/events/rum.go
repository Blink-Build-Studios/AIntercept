package events

import (
	"bytes"
	"compress/zlib"
	"encoding/json"
	"io"
	"strings"
)

// parseRUMEvents reads a Datadog browser RUM batch: newline-delimited JSON
// events, zlib-deflated when the SDK's dd-evp-encoding=deflate (signalled in
// the query string, not Content-Encoding). Each event is named by its type
// and, where one exists, its subtype: "view", "action:click",
// "resource:fetch", "error", "long_task", "vital:<name>".
func parseRUMEvents(raw []byte) []ClientEvent {
	if len(raw) > 1 && raw[0] == 0x78 { // zlib header
		if zr, err := zlib.NewReader(bytes.NewReader(raw)); err == nil {
			if plain, err := io.ReadAll(io.LimitReader(zr, maxDecodedBody)); err == nil {
				raw = plain
			}
		}
	}
	var out []ClientEvent
	for _, line := range strings.Split(string(raw), "\n") {
		var d map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(line)), &d) != nil {
			continue
		}
		typ, _ := d["type"].(string)
		name := typ
		sub := func(obj, key string) string {
			m, _ := d[obj].(map[string]any)
			s, _ := m[key].(string)
			return s
		}
		switch typ {
		case "action":
			name += ":" + sub("action", "type")
		case "resource":
			name += ":" + sub("resource", "type")
		case "vital":
			name += ":" + sub("vital", "name")
		}
		ce := ClientEvent{Type: "rum", Name: name, Data: d}
		ce.ID = sub(typ, "id")
		ce.SessionID = sub("session", "id")
		ce.UserID = sub("usr", "id")
		ce.Email = sub("usr", "email")
		out = append(out, ce)
	}
	return out
}
