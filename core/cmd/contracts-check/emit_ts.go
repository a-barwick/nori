package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var nonIdent = regexp.MustCompile(`[^A-Za-z0-9]+`)

func emitTS(dir, out string, fixtures []fixture) error {
	var body bytes.Buffer
	types := map[string]bool{}
	for _, fx := range fixtures {
		if !fx.Valid {
			continue
		}
		_, def, ok := strings.Cut(fx.Schema, "#/$defs/")
		if !ok {
			return fmt.Errorf("%s: schema %s does not name a $defs entry", fx.File, fx.Schema)
		}
		raw, err := os.ReadFile(filepath.Join(dir, fx.File))
		if err != nil {
			return err
		}
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, raw, "", "  "); err != nil {
			return fmt.Errorf("%s: %w", fx.File, err)
		}
		name := "fx_" + strings.Trim(nonIdent.ReplaceAllString(strings.TrimSuffix(strings.TrimPrefix(fx.File, "fixtures/"), ".json"), "_"), "_")
		types[def] = true
		fmt.Fprintf(&body, "export const %s: %s = %s;\n\n", name, def, strings.TrimSpace(pretty.String()))
	}
	names := make([]string, 0, len(types))
	for t := range types {
		names = append(names, t)
	}
	sort.Strings(names)
	var head bytes.Buffer
	fmt.Fprintf(&head, "import type { %s } from \"./assistant\";\n\n", strings.Join(names, ", "))
	return os.WriteFile(out, append(head.Bytes(), body.Bytes()...), 0o644)
}
