package pg

import (
	"fmt"
	"strings"
)

// sections head each kind of table in schema.sql.
var sections = map[Kind]string{
	Coordination: "§3.2 Coordination: the key rows (the commit), the flags, the workers, the tail.",
	Rendered:     "§3.3 Session tables: a session's rows of state.RenderSession, written by its own key.",
	Stored:       "§3.4 Stored inputs: the state's own rows, keyed by (key, the state's row id).",
	Fold:         "§3.4-3.5, §3.7 Batch parts, the directory folds and the request drawer.",
}

// DDL is the schema as schema.sql holds it, generated from Tables.
func DDL() string {
	var b strings.Builder
	b.WriteString(`-- The v7 schema (docs/PIPELINE.md §3). GENERATED from internal/pg/tables.go by
-- ` + "`go test ./internal/pg -run TestSchemaFile -update`" + `; do not edit. Applied by pg.Apply to an
-- empty database, which records this file's hash (SchemaHash) in schema_version. There are no
-- migrations: a change to this file means a fresh database.
`)
	last := Kind(-1)
	for _, t := range Tables {
		if t.Kind != last {
			fmt.Fprintf(&b, "\n-- %s\n", sections[t.Kind])
			last = t.Kind
		}
		lz4 := map[string]bool{}
		for _, c := range t.LZ4 {
			lz4[c] = true
		}
		fmt.Fprintf(&b, "\nCREATE TABLE %s (\n", t.Name)
		lines := make([]string, 0, len(t.Columns)+1)
		for _, c := range t.Columns {
			l := "    " + c.Name + " " + c.Type
			if lz4[c.Name] {
				l += " COMPRESSION lz4"
			}
			if c.NotNull {
				l += " NOT NULL"
			}
			lines = append(lines, l)
		}
		if len(t.PK) > 0 {
			lines = append(lines, "    PRIMARY KEY ("+strings.Join(t.PK, ", ")+")")
		}
		for _, r := range t.Refs {
			lines = append(lines, "    FOREIGN KEY "+r)
		}
		b.WriteString(strings.Join(lines, ",\n"))
		b.WriteString("\n)")
		if t.With != "" {
			b.WriteString(" WITH (" + t.With + ")")
		}
		b.WriteString(";\n")
		for _, ix := range t.Indexes {
			fmt.Fprintf(&b, "CREATE INDEX ON %s %s;\n", t.Name, ix)
		}
		if t.Seed != "" {
			b.WriteString(t.Seed + ";\n")
		}
	}
	return b.String()
}
