#!/usr/bin/env python3
"""Rejects docs site pages that break a rule a script can catch. Every finding fails.

Usage: check.py PAGE [PAGE ...]
       check.py --all      every page
       check.py --changed  pages that differ from origin/main
       check.py --live     pages that differ from the published site
"""
import re
import subprocess
import sys
from html.parser import HTMLParser
from pathlib import Path

SITE = Path(__file__).resolve().parents[4] / "docs" / "site"
LIVE_URL = "https://docs.noribot.chat/"
WORD_LIMIT = 800
VISIBLE_LIMIT = 500
LEDE_LIMIT = 20
OPENING_LIMIT = 30
SENTENCE_LIMIT = 30
SENTENCES_PER_BLOCK = 3
PROSE_CODE_LIMIT = 3
HEADING_LIMIT = 6
BOX_LIMIT = 15
LONG_PAGES = {"scenarios", "decisions", "reading", "sources", "words", "code", "open-questions"}
QUOTED_DATA_PAGES = {"scenarios"}
VISUALS = {"table", "svg", "img", "pre"}
VISUAL_CLASSES = {"flow", "states", "shots", "map", "say", "branch", "branch3", "branch4", "node", "seq", "road", "swatches"}
RAW = {"code", "pre", "script", "style", "svg"}
VOID = {"br", "hr", "img", "input", "link", "meta", "path", "circle", "rect", "line", "polygon"}

PHRASES = [
    (r"—", "em dash"),
    (r";", "semicolon"),
    (r"\b(?:sits? on|lives? in|sits? behind)\b", "figure of speech"),
    (r"\b(?:main checkout|gitignored|mode-600)\b", "instructions for contributors"),
    (r"\b[\w-]+, not (?:a |an |the )?[\w-]+", "'X, not Y' construction"),
    (r"\b(?:isn't|is not|aren't) [^.]{1,40}[,;] (?:it's|it is|they're)", "'isn't X, it's Y' construction"),
    (r"\b(?:real|actual) (?:work|question|point|problem)\b", "empty emphasis"),
    (r"\b(?:load-bearing|seamless\w*|robust\w*|leverag\w+|delve\w*|game-chang\w+|under the hood)\b", "stock phrase"),
    (r"\b(?:this (?:section|page) (?:explains|describes|covers)|it's worth noting|in short|simply put)\b", "narration"),
    (r"\bNori (?:notices|knows|thinks|understands|decides|cares|feels|wants|remembers)\b", "software described as a person"),
]
JARGON = r"\b(?:bounded|heartbeat|fenced|supersed\w+|admit\w*|admission|reconcil\w+|eligible|deterministic|idempotent|authoritative|exactly once|projects?|projection)\b"
PRAISE = r"\b(?:real|really|actual|actually|honest|honestly|true|truly|trust\w*|natural\w*|feels?|useful|meaningful\w*|powerful)\b"
SHORTHAND = r"\s(?:=|→|\+|≠)\s"


class Page(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.stack, self.text, self.prose, self.classes, self.links, self.order = [], [], [], set(), [], []
        self.title, self.heading_links, self.scripts, self.has_style = "", [], [], False
        self.block = None
        self.visible, self.lede, self.headings = [], [], []
        self.opening_words = None
        self.prose_code = 0
        self.lede_code = self.heading_code = False
        self.lede_depth = None

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        cls = (attrs.get("class") or "").split()
        self.classes.update(cls)
        self.order.append((tag, cls))
        if self.opening_words is None and "body" in self.stack and (tag in VISUALS or VISUAL_CLASSES & set(cls)):
            self.opening_words = len(" ".join(self.text).split())
        if tag == "p" and "lede" in cls:
            self.lede_depth = len(self.stack)
        if tag in ("h2", "h3"):
            self.headings.append([])
        if tag == "a" and attrs.get("href"):
            self.links.append(attrs["href"])
            if any(t in ("h1", "h2", "h3") for t in self.stack):
                self.heading_links.append(attrs["href"])
        if tag == "script" and attrs.get("src"):
            self.scripts.append(attrs["src"])
        if tag == "style":
            self.has_style = True
        if tag not in VOID:
            self.stack.append(tag)
        if tag in ("p", "li") and self.block is None:
            self.block = []

    def handle_endtag(self, tag):
        if tag in self.stack:
            while self.stack.pop() != tag:
                pass
        if tag == "p" and self.lede_depth == len(self.stack):
            self.lede_depth = None
        if tag in ("p", "li") and self.block is not None and tag not in self.stack:
            if not {"p", "li"} & set(self.stack):
                self.prose.append("".join(self.block).strip())
                self.block = None

    def handle_data(self, data):
        if "head" in self.stack:
            if "title" in self.stack:
                self.title += data
            return
        in_heading = {"h2", "h3"} & set(self.stack)
        if "code" in self.stack:
            self.lede_code = self.lede_code or self.lede_depth is not None
            self.heading_code = self.heading_code or bool(in_heading)
            if self.block is not None and "details" not in self.stack:
                self.prose_code += 1
        if RAW & set(self.stack):
            if self.block is not None and "code" in self.stack:
                self.block.append("`code`")
            return
        self.text.append(data)
        if "details" not in self.stack:
            self.visible.append(data)
        if self.lede_depth is not None:
            self.lede.append(data)
        if in_heading and self.headings:
            self.headings[-1].append(data)
        if self.block is not None:
            self.block.append(data)


def known_classes():
    css = (SITE / "site.css").read_text(encoding="utf-8")
    js = (SITE / "site.js").read_text(encoding="utf-8")
    return set(re.findall(r"\.([a-zA-Z][\w-]*)", css)) | set(re.findall(r"class(?:Name)?=?[\"' ]+([\w-]+)", js))


def total_words(page):
    return len(" ".join(page.text).split())


def sentences(block):
    return [s for s in re.split(r"(?<=[.!?])\s+", block) if s.strip()]


def check(path):
    fails = []
    source = path.read_text(encoding="utf-8")
    page = Page()
    page.feed(source)
    name = path.stem
    tags = [tag for tag, _ in page.order]

    for needed, label in (('href="site.css"', "site.css link"), ('src="site.js"', "site.js script"), ('name="viewport"', "viewport meta")):
        if needed not in source:
            fails.append(f"missing {label}")
    if not page.title.endswith("· Nori"):
        fails.append("title must end with '· Nori'")
    if tags.count("h1") != 1:
        fails.append("page needs exactly one h1")
    if "lede" not in page.classes:
        fails.append("missing p.lede")
    if "github.com" in source:
        fails.append("links GitHub, but the repo is private: use a plain path")
    if re.search(r"(?<![a-z])nova", source, re.I):
        fails.append("uses the name Nova")
    if "sources" not in page.classes and name not in LONG_PAGES and name != "index":
        fails.append("missing details.sources")
    if f"['{name}'," not in (SITE / "site.js").read_text(encoding="utf-8"):
        fails.append("page isn't in NAV in site.js")

    reference = name in LONG_PAGES
    lede_words = len(" ".join(page.lede).split())
    if lede_words > LEDE_LIMIT:
        fails.append(f"lede has {lede_words} words, limit {LEDE_LIMIT}")
    if page.lede_code:
        fails.append("code in the lede")
    if page.heading_code:
        fails.append("code in a heading")
    for heading in page.headings:
        text = " ".join("".join(heading).split())
        if len(text.split()) > HEADING_LIMIT:
            fails.append(f"heading over {HEADING_LIMIT} words: {text}")

    if not reference:
        if page.opening_words is None:
            fails.append("no visual on the page")
        elif page.opening_words > OPENING_LIMIT:
            fails.append(f"{page.opening_words} words before the first visual, limit {OPENING_LIMIT}")
        if page.prose_code > PROSE_CODE_LIMIT:
            fails.append(f"{page.prose_code} code names in visible paragraphs and lists, limit {PROSE_CODE_LIMIT}: move them to a table, Data button or details.more")

    words = total_words(page)
    visible = len(" ".join(page.visible).split())
    for box in re.findall(r'class="node[^"]*">(.*?)</div>', source, re.S):
        text = re.sub(r"<[^>]+>", " ", box)
        if len(text.split()) > BOX_LIMIT:
            fails.append(f"diagram box over {BOX_LIMIT} words: {' '.join(text.split()[:6])} ...")
    if not reference:
        if visible > VISIBLE_LIMIT:
            fails.append(f"{visible} visible words, limit {VISIBLE_LIMIT}: cut, or move detail to details.more")
        if words > WORD_LIMIT:
            fails.append(f"{words} words in total, limit {WORD_LIMIT}: split it by topic")
        jargon = sorted({m.lower() for m in re.findall(JARGON, " ".join(page.visible), re.I)})
        if jargon:
            fails.append(f"implementation terms outside details: {', '.join(jargon)}")
    if not reference and [cls for tag, cls in page.order if tag == "details"].count(["more"]) > 1:
        fails.append("more than one details.more section")

    if page.heading_links:
        fails.append("heading contains a link: only the copy icon is interactive")
    for src in page.scripts:
        if src.startswith(("http:", "https:", "//")):
            fails.append(f"external script: {src}")
    if page.has_style:
        fails.append("inline <style>: use site.css classes")
    unknown = sorted(page.classes - known_classes())
    if unknown:
        fails.append(f"classes not in site.css or site.js: {', '.join(unknown)}")

    for link in page.links:
        target = link.split("#")[0]
        if link.startswith("/"):
            fails.append(f"use a relative link: {link}")
        elif target and not re.match(r"[a-z]+:", target) and not (SITE / target).exists():
            fails.append(f"broken link: {link}")

    if re.search(r"[\w.+-]+@(?!example\.invalid)[\w-]+\.\w+|\bsk-[A-Za-z0-9]{8,}", source):
        fails.append("possible email address or API key")

    for block in [] if name in QUOTED_DATA_PAGES else page.prose:
        block = re.sub(r"“[^”]*”", "“”", block)
        short = block if len(block) < 90 else block[:87] + "..."
        for pattern, label in PHRASES:
            if re.search(pattern, block, re.I):
                fails.append(f"{label}: {short}")
        for match in re.findall(PRAISE, block, re.I):
            fails.append(f"'{match}' is emphasis: {short}")
        if re.search(SHORTHAND, block):
            fails.append(f"symbol used as shorthand: {short}")
        parts = sentences(block)
        if len(parts) > SENTENCES_PER_BLOCK:
            fails.append(f"{len(parts)} sentences in one block, limit {SENTENCES_PER_BLOCK}: {short}")
        for part in parts:
            if len(part.split()) > SENTENCE_LIMIT:
                fails.append(f"sentence over {SENTENCE_LIMIT} words: {part[:87]}...")
    return words, fails


def changed_pages():
    root = SITE.parents[1]
    git = lambda *args: subprocess.run(["git", *args], cwd=root, check=True, capture_output=True, text=True).stdout.split()
    base = git("merge-base", "origin/main", "HEAD")[0]
    names = git("diff", "--name-only", base, "--", "docs/site/*.html") + git("ls-files", "--others", "--exclude-standard", "--", "docs/site/*.html")
    return [p for p in (root / name for name in sorted(set(names))) if p.exists()]


def undeployed_pages():
    pages = []
    for path in sorted(SITE.glob("*.html")):
        result = subprocess.run(["curl", "-sS", "--max-time", "15", "-w", "%{http_code}", LIVE_URL + path.name], capture_output=True)
        body, status = result.stdout[:-3], result.stdout[-3:]
        if result.returncode or status not in (b"200", b"404"):
            sys.exit(f"Can't compare {path.name} with the published site: {result.stderr.decode().strip() or status.decode()}")
        if status == b"404" or body != path.read_bytes():
            pages.append(path)
    return pages


def main(argv):
    if not argv:
        print(__doc__)
        return 2
    selected = {"--all": lambda: sorted(SITE.glob("*.html")), "--changed": changed_pages, "--live": undeployed_pages}
    paths = selected[argv[0]]() if argv[0] in selected else [Path(a) for a in argv]
    if not paths:
        print("No pages to check.")
    failed = False
    for path in paths:
        if not path.exists():
            print(f"{path}: not found")
            failed = True
            continue
        words, fails = check(path.resolve())
        print(f"{path.name}: {'PASS' if not fails else 'REJECT'}, {words} words, {len(fails)} findings")
        for line in fails:
            print(f"  FAIL {line}")
        failed = failed or bool(fails)
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
