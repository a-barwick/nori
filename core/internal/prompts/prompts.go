// Package prompts holds the instructions, tool descriptions and runtime strings Nori sends to models, as text files
// built into the binary. Each file's final newline is not part of its text. A top-level .txt file can include another
// with {{template "name"}}, where name is the other file's name without .txt. Files in runtime/ are short strings the
// model reads in tool results, notes and task requests; their {{.Field}} placeholders are filled when the message is
// built.
package prompts

import (
	"embed"
	"path"
	"strings"
	"text/template"
)

//go:embed *.txt *.json task-tools/*.txt runtime/*.txt
var files embed.FS

var (
	Conversation      = render("conversation")
	ConversationTools = read("conversation-tools.json")
	Introduction      = render("introduction")
	Compaction        = render("compaction")
	CommandPlan       = render("command-plan")
	TaskWorker        = render("task-worker")

	taskTools = func() map[string]string {
		entries, err := files.ReadDir("task-tools")
		if err != nil {
			panic(err)
		}
		tools := map[string]string{}
		for _, entry := range entries {
			tools[strings.TrimSuffix(entry.Name(), ".txt")] = read(path.Join("task-tools", entry.Name()))
		}
		return tools
	}()
)

// TaskTool returns the description of one task worker tool. Every tool the worker offers has a file, so a missing
// one is a build mistake.
func TaskTool(name string) string {
	description, ok := taskTools[name]
	if !ok {
		panic("prompts: no description for task tool " + name)
	}
	return description
}

var runtimeStrings = func() map[string]*template.Template {
	entries, err := files.ReadDir("runtime")
	if err != nil {
		panic(err)
	}
	set := map[string]*template.Template{}
	for _, entry := range entries {
		name := strings.TrimSuffix(entry.Name(), ".txt")
		set[name] = template.Must(template.New(name).Option("missingkey=error").Parse(read(path.Join("runtime", entry.Name()))))
	}
	return set
}()

// Runtime renders one runtime string with data, which may be nil when the string has no placeholders. A missing
// file or field is a build mistake.
func Runtime(name string, data any) string {
	t, ok := runtimeStrings[name]
	if !ok {
		panic("prompts: no runtime string " + name)
	}
	var out strings.Builder
	if err := t.Execute(&out, data); err != nil {
		panic(err)
	}
	return out.String()
}

func read(name string) string {
	data, err := files.ReadFile(name)
	if err != nil {
		panic(err)
	}
	return strings.TrimSuffix(string(data), "\n")
}

var templates = func() *template.Template {
	set := template.New("")
	entries, err := files.ReadDir(".")
	if err != nil {
		panic(err)
	}
	for _, entry := range entries {
		if name, ok := strings.CutSuffix(entry.Name(), ".txt"); ok {
			template.Must(set.New(name).Parse(read(entry.Name())))
		}
	}
	return set
}()

func render(name string) string {
	var out strings.Builder
	if err := templates.ExecuteTemplate(&out, name, nil); err != nil {
		panic(err)
	}
	return out.String()
}
