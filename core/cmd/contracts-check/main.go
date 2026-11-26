package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"nori/internal/api"
)

const schemaHost = "contracts.nori.local"

type index struct {
	Revision   string    `json:"revision"`
	SchemasDir string    `json:"schemas_dir"`
	Fixtures   []fixture `json:"fixtures"`
}

type fixture struct {
	File   string `json:"file"`
	Schema string `json:"schema"`
	Valid  bool   `json:"valid"`
	Note   string `json:"note,omitempty"`
}

type dirLoader struct{ dir string }

func (l dirLoader) Load(rawURL string) (any, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if u.Host != schemaHost {
		return nil, fmt.Errorf("refusing to load %s: only %s schemas resolve locally", rawURL, schemaHost)
	}
	name := filepath.Base(u.Path)
	f, err := os.Open(filepath.Join(l.dir, name))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return jsonschema.UnmarshalJSON(f)
}

func main() {
	root := flag.String("root", "", "path to contracts/assistant (default: found by walking up from the working directory)")
	strict := flag.Bool("strict", false, "fail when a $defs entry has no fixture")
	live := flag.String("live", "", "run scenarios against this core base URL (e.g. http://127.0.0.1:8083)")
	emit := flag.String("emit-ts", "", "write every valid fixture as a typed TypeScript literal to this file")
	only := flag.String("only", "", "with -live, run only scenarios whose file name contains this text")
	flag.Parse()
	dir := *root
	if dir == "" {
		dir = findRoot()
	}
	if dir == "" {
		fatal("contracts/assistant not found; pass -root")
	}
	var idx index
	raw, err := os.ReadFile(filepath.Join(dir, "fixtures", "index.json"))
	if err != nil {
		fatal(err.Error())
	}
	if err := json.Unmarshal(raw, &idx); err != nil {
		fatal("fixtures/index.json: " + err.Error())
	}
	schemasDir := filepath.Join(dir, idx.SchemasDir)
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()
	compiler.UseLoader(dirLoader{dir: schemasDir})

	failures := 0
	defs := map[string]bool{}
	files, _ := filepath.Glob(filepath.Join(schemasDir, "*.schema.json"))
	sort.Strings(files)
	for _, f := range files {
		name := filepath.Base(f)
		if _, err := compiler.Compile("https://" + schemaHost + "/assistant/" + name); err != nil {
			failures++
			fmt.Printf("FAIL schema %s: %v\n", name, err)
			continue
		}
		b, _ := os.ReadFile(f)
		var doc struct {
			Defs map[string]json.RawMessage `json:"$defs"`
		}
		_ = json.Unmarshal(b, &doc)
		for d := range doc.Defs {
			defs[name+"#/$defs/"+d] = false
		}
	}
	roundTrips := 0
	for _, fx := range idx.Fixtures {
		defs[fx.Schema] = true
		sch, err := compiler.Compile("https://" + schemaHost + "/assistant/" + fx.Schema)
		if err != nil {
			failures++
			fmt.Printf("FAIL %s: schema %s: %v\n", fx.File, fx.Schema, err)
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, fx.File))
		if err != nil {
			failures++
			fmt.Printf("FAIL %s: %v\n", fx.File, err)
			continue
		}
		value, err := jsonschema.UnmarshalJSON(strings.NewReader(string(raw)))
		if err != nil {
			failures++
			fmt.Printf("FAIL %s: not JSON: %v\n", fx.File, err)
			continue
		}
		verr := sch.Validate(value)
		switch {
		case fx.Valid && verr != nil:
			failures++
			fmt.Printf("FAIL %s: expected valid against %s\n%s\n", fx.File, fx.Schema, indent(verr.Error()))
		case !fx.Valid && verr == nil:
			failures++
			fmt.Printf("FAIL %s: expected invalid against %s but validated\n", fx.File, fx.Schema)
		case fx.Valid:
			if _, registered := api.ContractTypes[fx.Schema]; !registered {
				break
			}
			again, err := api.RoundTrip(fx.Schema, raw)
			if err == nil {
				var reencoded any
				if reencoded, err = jsonschema.UnmarshalJSON(strings.NewReader(string(again))); err == nil {
					err = sch.Validate(reencoded)
				}
			}
			if err != nil {
				failures++
				fmt.Printf("FAIL %s: Go round trip through %s\n%s\n", fx.File, fx.Schema, indent(err.Error()))
			} else {
				roundTrips++
			}
		}
	}
	compile := func(ref string) (*jsonschema.Schema, error) {
		return compiler.Compile("https://" + schemaHost + "/assistant/" + ref)
	}
	scenarioFiles, scenarios, err := loadScenarios(dir)
	if err != nil {
		failures++
		fmt.Printf("FAIL scenarios: %v\n", err)
	}
	failures += checkScenariosOffline(scenarioFiles, scenarios, compile)
	if *live != "" {
		failures += runScenariosLive(*live, scenarioFiles, scenarios, compile, *only)
	}
	if *emit != "" {
		if err := emitTS(dir, *emit, idx.Fixtures); err != nil {
			failures++
			fmt.Printf("FAIL emit-ts: %v\n", err)
		}
	}
	var uncovered []string
	for d, covered := range defs {
		if !covered {
			uncovered = append(uncovered, d)
		}
	}
	sort.Strings(uncovered)
	for _, d := range uncovered {
		fmt.Printf("WARN no fixture for %s\n", d)
	}
	fmt.Printf("contracts %s: %d schemas, %d fixtures (%d round-tripped through Go types), %d scenarios, %d failures, %d definitions without fixtures\n", idx.Revision, len(files), len(idx.Fixtures), roundTrips, len(scenarios), failures, len(uncovered))
	if failures > 0 || (*strict && len(uncovered) > 0) {
		os.Exit(1)
	}
}

func findRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	for d := wd; ; d = filepath.Dir(d) {
		candidate := filepath.Join(d, "contracts", "assistant")
		if st, err := os.Stat(filepath.Join(candidate, "fixtures", "index.json")); err == nil && !st.IsDir() {
			return candidate
		}
		if filepath.Dir(d) == d {
			return ""
		}
	}
}

func indent(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := range lines {
		lines[i] = "    " + lines[i]
	}
	return strings.Join(lines, "\n")
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "contracts-check:", msg)
	os.Exit(2)
}
