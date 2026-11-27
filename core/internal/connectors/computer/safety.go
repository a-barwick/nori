package computer

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"sync"

	"nori/internal/state"
)

var (
	domainTokenRe = regexp.MustCompile(`(?i)\b(?:https?://)?([a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+)`)
	urlTokenRe    = regexp.MustCompile(`(?i)https?://[^\s"'<>]+`)

	// Commands that perform irreversible external writes/publishes/destructive deletes.
	sensitiveShellPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bgit\s+push\b`),
		regexp.MustCompile(`(?i)\bgh\s+(pr|issue|release)\s+(create|merge|close|delete|comment)\b`),
		regexp.MustCompile(`(?i)\bnpm\s+publish\b`),
		regexp.MustCompile(`(?i)\btwine\s+upload\b`),
		regexp.MustCompile(`(?i)\bcargo\s+publish\b`),
		regexp.MustCompile(`(?i)\b(sendmail|msmtp|mailx)\b`),
		regexp.MustCompile(`(?i)\bcurl\b.*(\s-X\s*(POST|PUT|PATCH|DELETE)\b|\s--request\s*(POST|PUT|PATCH|DELETE)\b|\s(-d|--data|--data-raw|--data-binary|--data-urlencode|-F|--form|-T|--upload-file)\b)`),
		regexp.MustCompile(`(?i)\bwget\b.*(--post-data|--post-file|--method=(POST|PUT|PATCH|DELETE))`),
		regexp.MustCompile(`(?i)\b(scp|sftp)\b`),
		regexp.MustCompile(`(?i)\brsync\b.*:`),
		regexp.MustCompile(`(?i)\brm\s+-[a-zA-Z]*r[a-zA-Z]*f[a-zA-Z]*\s+(/home/nori/?($|\s)|/home/nori/workspace/?($|\s)|~/?($|\s)|\.\s*$|\*\s*$)`),
	}

	// Commands that can open outbound network connections or run arbitrary code/binaries.
	networkShellPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b(curl|wget|nc|ncat|netcat|socat|ssh|scp|sftp|rsync|ftp|telnet|dig|nslookup|host)\b`),
		regexp.MustCompile(`(?i)\bgit\s+(clone|fetch|pull|push|ls-remote)\b`),
		regexp.MustCompile(`(?i)\b(urllib|requests|httpx|aiohttp|http\.client|socket\.create_connection|fetch\(|axios)\b`),
		regexp.MustCompile(`(?i)/dev/tcp/`),
	}

	// Any shell metacharacter or subshell/redirection operator disqualifies a command from read-only mode.
	shellMetaRe = regexp.MustCompile(`([|;&<>` + "`" + `\n\r]|\$\(|\$\{)`)

	// Strict, argument-validated read-only binaries (cannot spawn arbitrary subcommands or write files).
	strictReadOnlyBins = map[string]bool{
		"ls": true, "pwd": true, "cat": true, "head": true, "tail": true,
		"wc": true, "stat": true, "file": true, "uname": true, "date": true,
		"id": true, "df": true, "du": true, "pdfinfo": true,
	}

	// Safe offline workspace-local tools that can skip AutoReviewer on untainted user tasks.
	safeLocalWriteBins = map[string]bool{
		"mkdir": true, "cp": true, "mv": true, "touch": true, "typst": true,
		"pandoc": true, "qpdf": true, "pdftotext": true, "libreoffice": true,
	}

	sensitiveBrowserKeywords = []string{
		"submit", "send", "checkout", "pay", "purchase", "buy now", "place order",
		"confirm order", "confirm", "delete", "remove permanently", "publish", "post",
		"transfer", "wire", "approve", "authorize", "revoke", "disconnect", "unsubscribe",
	}
)

// isReadOnlyShellCommand validates strictly that cmd is a single simple invocation of a
// side-effect-free binary with no shell metacharacters, no output/exec flags, and no path traversal.
func isReadOnlyShellCommand(cmd string) bool {
	trimmed := strings.TrimSpace(cmd)
	if trimmed == "" || shellMetaRe.MatchString(trimmed) {
		return false
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return false
	}
	bin := strings.ToLower(fields[0])
	if !strictReadOnlyBins[bin] {
		return false
	}
	for _, arg := range fields[1:] {
		lower := strings.ToLower(arg)
		if strings.Contains(arg, "..") {
			return false
		}
		if strings.HasPrefix(lower, "-o") || strings.HasPrefix(lower, "--output") ||
			strings.HasPrefix(lower, "-i") || strings.HasPrefix(lower, "--in-place") ||
			strings.HasPrefix(lower, "-exec") || strings.HasPrefix(lower, "--exec") {
			return false
		}
	}
	return true
}

// isKnownOfflineLocalCommand reports whether cmd is a single known workspace-local document/file
// command without network access (safe to skip AutoReviewer when untainted).
func isKnownOfflineLocalCommand(cmd string) bool {
	trimmed := strings.TrimSpace(cmd)
	if trimmed == "" || shellMetaRe.MatchString(trimmed) || hasExternalNetworkCommand(trimmed) {
		return false
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return false
	}
	return safeLocalWriteBins[strings.ToLower(fields[0])]
}

func isSensitiveShellCommand(cmd string) (bool, string) {
	for _, re := range sensitiveShellPatterns {
		if re.MatchString(cmd) {
			return true, "This command performs an external write, publish, or destructive delete."
		}
	}
	return false, ""
}

func hasExternalNetworkCommand(cmd string) bool {
	for _, re := range networkShellPatterns {
		if re.MatchString(cmd) {
			return true
		}
	}
	return false
}

func extractDomainsFromTexts(texts []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, text := range texts {
		for _, m := range domainTokenRe.FindAllStringSubmatch(text, -1) {
			if len(m) < 2 {
				continue
			}
			d := state.NormalizeDomain(m[1])
			if d == "" || isCommonFileExtDomain(d) || seen[d] {
				continue
			}
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

func extractURLsFromText(text string) []string {
	return urlTokenRe.FindAllString(text, -1)
}

func isCommonFileExtDomain(d string) bool {
	for _, ext := range []string{
		".pdf", ".csv", ".ics", ".xlsx", ".xls", ".docx", ".doc", ".pptx",
		".png", ".jpg", ".jpeg", ".webp", ".gif", ".svg", ".txt", ".md",
		".json", ".yaml", ".yml", ".py", ".js", ".ts", ".go", ".sh", ".html", ".htm", ".zip", ".tar", ".gz",
	} {
		if strings.HasSuffix(d, ext) {
			return true
		}
	}
	return false
}

func isDomainTrusted(target string, userSources []string, trustedDomains []string) bool {
	norm := state.NormalizeDomain(target)
	if norm == "" {
		return false
	}
	for _, d := range trustedDomains {
		if state.DomainMatches(norm, d) {
			return true
		}
	}
	for _, d := range extractDomainsFromTexts(userSources) {
		if state.DomainMatches(norm, d) {
			return true
		}
	}
	return false
}

type resolvedBrowserElement struct {
	OK         bool   `json:"ok"`
	Found      bool   `json:"found"`
	URL        string `json:"url"`
	Title      string `json:"title"`
	Tag        string `json:"tag"`
	Role       string `json:"role"`
	Type       string `json:"type"`
	Label      string `json:"label"`
	Href       string `json:"href"`
	IsSubmit   bool   `json:"is_submit"`
	FormAction string `json:"form_action"`
}

func isSensitiveBrowserAction(action string, args map[string]any, resolved *resolvedBrowserElement) (bool, string) {
	if submit, _ := args["submit"].(bool); submit {
		return true, "Submitting a web form externally."
	}
	intent, _ := args["intent"].(string)
	lowerIntent := strings.ToLower(strings.TrimSpace(intent))
	switch lowerIntent {
	case "submit", "send", "checkout", "pay", "purchase", "delete", "publish", "post", "confirm":
		return true, "Performing an external browser " + lowerIntent + " action."
	}
	if resolved != nil && resolved.IsSubmit && (action == "click" || action == "press") {
		return true, "Submitting an external web form."
	}
	if action == "click" || action == "press" {
		label, _ := args["target_label"].(string)
		selector, _ := args["selector"].(string)
		key, _ := args["key"].(string)
		resolvedLabel := ""
		resolvedHref := ""
		resolvedForm := ""
		if resolved != nil {
			resolvedLabel = resolved.Label
			resolvedHref = resolved.Href
			resolvedForm = resolved.FormAction
		}
		combined := strings.ToLower(strings.Join([]string{label, selector, resolvedLabel, resolvedHref, resolvedForm}, " "))
		for _, kw := range sensitiveBrowserKeywords {
			if strings.Contains(combined, kw) {
				return true, "Clicking or triggering an irreversible action (" + kw + ")."
			}
		}
		if action == "press" && strings.EqualFold(strings.TrimSpace(key), "enter") {
			return true, "Pressing Enter on a web page may submit an external form or message."
		}
	}
	return false, ""
}

// loopTracker detects 2-step identical browser states (url, axtree_hash, action) per run,
// and blocks repeating an identical failed/no-op action BEFORE executing a 3rd time.
type loopTracker struct {
	mu      sync.Mutex
	history map[string][]string // runID -> recent (url, axtree, action) fingerprints
	lastSig map[string]string   // runID -> last (url, action) that triggered loop
}

func newLoopTracker() *loopTracker {
	return &loopTracker{
		history: map[string][]string{},
		lastSig: map[string]string{},
	}
}

func (l *loopTracker) isBlockedBeforeAction(runID, actionSig string) bool {
	if runID == "" || actionSig == "" {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lastSig[runID] != "" && l.lastSig[runID] == actionSig
}

func (l *loopTracker) recordAndCheck(runID, url, axtree, actionSig string) bool {
	if runID == "" {
		return false
	}
	sum := sha256.Sum256([]byte(url + "\x00" + axtree + "\x00" + actionSig))
	fp := hex.EncodeToString(sum[:12])

	l.mu.Lock()
	defer l.mu.Unlock()
	list := l.history[runID]
	list = append(list, fp)
	if len(list) > 8 {
		list = list[len(list)-8:]
	}
	l.history[runID] = list

	n := len(list)
	looped := (n >= 2 && list[n-1] == list[n-2]) ||
		(n >= 4 && list[n-1] == list[n-3] && list[n-2] == list[n-4])
	if looped {
		l.lastSig[runID] = actionSig
	} else {
		delete(l.lastSig, runID)
	}
	return looped
}
