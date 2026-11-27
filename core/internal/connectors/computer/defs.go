package computer

import (
	"context"
	"encoding/json"
	"strings"

	"nori/internal/extensions"
)

const (
	Version         = "1"
	CapabilityID    = "personal_computer"
	CapabilityTitle = "Personal computer"
)

type toolSpec struct {
	name, title, target string
	effect              extensions.Effect
	retry               string
	timeoutMS           int
	interruptedCostUSD  float64
	description, schema string
}

var toolSpecs = []toolSpec{
	{
		name:               "computer_shell",
		title:              "Run a shell command on the user's computer",
		target:             "nori.computer/shell",
		effect:             extensions.EffectReversibleWrite,
		retry:              "reconcile_only",
		timeoutMS:          95_000,
		interruptedCostUSD: 0.005,
		description:        "Run a bash command in the user's persistent Linux computer (/home/nori/workspace). Supports scripts, git, installing CLI packages (pip/npm/apt), and document/data processing (pandas, openpyxl, pdfplumber, pypdf, typst, pandoc, libreoffice, qpdf, ffmpeg). Caps each call at 90s; set background=true for long installs/builds and poll with action='job_status' or stop with action='job_cancel'. External pushes, sends, destructive deletes, and non-read-only commands after reading untrusted content require approval.",
		schema: `{
			"type": "object",
			"properties": {
				"action": {"type": "string", "enum": ["run", "job_status", "job_cancel"], "description": "Default 'run'. Use 'job_status' or 'job_cancel' with job_id to inspect or stop a background command."},
				"command": {"type": "string", "description": "Bash command to run via bash -lc (required when action is 'run')."},
				"work_dir": {"type": "string", "description": "Working directory inside /home/nori (defaults to /home/nori/workspace)."},
				"timeout_seconds": {"type": "integer", "minimum": 1, "maximum": 90, "description": "Optional timeout cap (default 60, max 90)."},
				"background": {"type": "boolean", "description": "Run asynchronously and return a job_id immediately."},
				"job_id": {"type": "string", "description": "Background job_id returned by an earlier background call."}
			},
			"additionalProperties": false
		}`,
	},
	{
		name:        "computer_files",
		title:       "Manage files on the user's computer",
		target:      "nori.computer/files",
		effect:      extensions.EffectReversibleWrite,
		retry:       "reconcile_only",
		timeoutMS:   45_000,
		description: "List, read, write, or delete files inside /home/nori, stage a task upload into /home/nori/workspace/uploads/ (stage_upload), or publish a generated file (PDF, XLSX, PNG, HTML, CSV, etc.) into the user's Nori Library (publish_result).",
		schema: `{
			"type": "object",
			"properties": {
				"action": {"type": "string", "enum": ["list", "read", "write", "delete", "stage_upload", "publish_result"]},
				"path": {"type": "string", "description": "Path inside /home/nori (relative paths resolve under /home/nori/workspace)."},
				"content": {"type": "string", "description": "File contents for action='write'."},
				"encoding": {"type": "string", "enum": ["utf8", "base64"], "description": "Encoding for write/read (default utf8)."},
				"upload_id": {"type": "string", "description": "Task upload ID for action='stage_upload'."},
				"title": {"type": "string", "description": "Library document title for action='publish_result' (1-200 bytes)."},
				"entity_tags": {"type": "array", "items": {"type": "string"}, "description": "Optional entity tags for forget cleanup linking."}
			},
			"required": ["action"],
			"additionalProperties": false
		}`,
	},
	{
		name:               "computer_browser",
		title:              "Control the browser on the user's computer",
		target:             "nori.computer/browser",
		effect:             extensions.EffectReversibleWrite,
		retry:              "reconcile_only",
		timeoutMS:          60_000,
		interruptedCostUSD: 0.01,
		description:        "Drive the persistent headed Chromium browser on the user's computer. Defaults to returning a pruned Accessibility Tree with [ref] handles and text to keep context small. Use action='screenshot' with a question to inspect the viewport through Nori's isolated vision inspector. Final form submissions, sends, checkouts, payments, and navigating to novel domains after reading untrusted content require approval. If a login wall, 2FA, or CAPTCHA blocks progress, call computer_request_takeover.",
		schema: `{
			"type": "object",
			"properties": {
				"action": {"type": "string", "enum": ["open", "snapshot", "click", "type", "press", "select", "scroll", "back", "wait", "screenshot", "downloads"]},
				"url": {"type": "string", "description": "URL for action='open'."},
				"ref": {"type": "integer", "minimum": 1, "description": "Element reference number from the latest snapshot."},
				"selector": {"type": "string", "description": "Optional CSS selector fallback."},
				"text": {"type": "string", "description": "Text to enter for action='type'."},
				"clear": {"type": "boolean", "description": "Clear existing input before typing (default true)."},
				"submit": {"type": "boolean", "description": "Press Enter after typing (triggers approval if submitting externally)."},
				"key": {"type": "string", "description": "Key name for action='press' (e.g. Enter, Escape, Tab)."},
				"value": {"type": "string", "description": "Option value for action='select'."},
				"delta_y": {"type": "integer", "description": "Vertical scroll pixels for action='scroll' (default 600)."},
				"ms": {"type": "integer", "minimum": 100, "maximum": 15000, "description": "Milliseconds for action='wait'."},
				"question": {"type": "string", "description": "Question for isolated vision inspection on action='screenshot'."},
				"full_page": {"type": "boolean", "description": "Capture full page screenshot."},
				"intent": {"type": "string", "description": "Optional intent hint (e.g. 'navigate', 'draft', 'submit', 'send', 'checkout', 'pay', 'delete')."},
				"target_label": {"type": "string", "description": "Human-readable label of the element being clicked/submitted."}
			},
			"required": ["action"],
			"additionalProperties": false
		}`,
	},
	{
		name:        "computer_request_takeover",
		title:       "Ask the user to take control of their computer desktop",
		target:      "nori.computer/request_takeover",
		effect:      extensions.EffectReversibleWrite,
		retry:       "reconcile_only",
		timeoutMS:   30_000,
		description: "Pause the task cleanly and ask the user to open their live computer desktop (e.g., to sign into a site, complete 2FA, or solve a CAPTCHA). The task resumes automatically when the user clicks Done.",
		schema: `{
			"type": "object",
			"properties": {
				"prompt": {"type": "string", "minLength": 1, "maxLength": 1000, "description": "Plain-language instruction for the user (e.g. 'Sign into Gmail on mail.google.com, then click Done.')."},
				"url": {"type": "string", "description": "Optional URL to open in the desktop browser before handing over control."},
				"reason": {"type": "string", "enum": ["login", "2fa", "captcha", "verification", "manual_step"], "description": "Why user takeover is needed."}
			},
			"required": ["prompt"],
			"additionalProperties": false
		}`,
	},
}

var Definitions = func() []extensions.Definition {
	out := make([]extensions.Definition, 0, len(toolSpecs))
	for _, s := range toolSpecs {
		var compact any
		if err := json.Unmarshal([]byte(s.schema), &compact); err != nil {
			panic("computer tool schema " + s.name + ": " + err.Error())
		}
		schema, _ := json.Marshal(compact)
		d, err := extensions.Seal(extensions.Definition{
			Ref:             extensions.DefinitionRef{ID: "nori/computer/" + strings.ReplaceAll(s.name, "_", "-"), Version: Version},
			Extension:       extensions.DefinitionRef{ID: "nori/computer", Version: Version},
			ModelName:       s.name,
			Title:           s.title,
			Description:     s.description,
			Capability:      CapabilityID,
			CapabilityTitle: CapabilityTitle,
			InputSchema:     schema,
			Binding:         extensions.Binding{Adapter: extensions.AdapterNative, Target: s.target, Version: Version},
			Policy:          extensions.Policy{Effect: s.effect, Retry: s.retry, TimeoutMS: s.timeoutMS, Location: extensions.LocationRuntime, InterruptedCostUSD: s.interruptedCostUSD},
			Exposure:        extensions.ExposureDirect,
		})
		if err != nil {
			panic(err)
		}
		out = append(out, d)
	}
	return out
}()

// Source exposes Personal Computer tools to the extension catalog when enabled.
type Source struct {
	Enabled bool
}

func (s Source) Inventory(_ context.Context) (extensions.Inventory, error) {
	if !s.Enabled {
		return extensions.Inventory{}, nil
	}
	return extensions.Inventory{
		Definitions: Definitions,
		Capabilities: []extensions.CapabilityState{
			{
				Capability:   CapabilityID,
				Title:        CapabilityTitle,
				Account:      "user's persistent Linux computer (/home/nori)",
				Availability: "available",
			},
		},
	}, nil
}
