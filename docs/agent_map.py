#!/usr/bin/env python3
"""Lets agents query the docs site without reading all of it.

Usage: python3 docs/agent_map.py find "budget continue"   sections that match, as plain text
       python3 docs/agent_map.py code core/internal/task/workflow.go   pages that describe a file
       python3 docs/agent_map.py stale                    pages whose code changed since their review
       python3 docs/agent_map.py mark PAGE [PAGE ...]     record a review PASS at the current commit
       python3 docs/agent_map.py build                    regenerate docs/agent-map.json
       python3 docs/agent_map.py check                    fail if the map is out of date, warn on stale pages
"""
import json
import re
import subprocess
import sys
from html.parser import HTMLParser
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SITE = ROOT / "docs" / "site"
MAP = ROOT / "docs" / "agent-map.json"
REVIEWED = ROOT / "docs" / "reviewed.json"
FEATURES = {"find", "code", "stale"}
SKIP = {"script", "style", "head", "defs"}
VOID = {"br", "hr", "img", "input", "link", "meta", "path", "circle", "rect", "line", "polygon", "use", "ellipse"}
BLOCK = {"p", "li", "tr", "h1", "h2", "h3", "div", "section", "details", "summary", "text", "pre", "ul", "table"}
FIND_CHARS = 1800
FIND_LIMIT = 3
STALE_LINES = 8
STOP = {"a", "an", "and", "are", "does", "how", "in", "is", "it", "of", "on", "the", "to", "what", "when", "which", "with"}


def slug(text):
    return re.sub(r"[^a-z0-9]+", "-", text.lower()).strip("-")


class Page(HTMLParser):
    """Splits a page into h2 sections, keeping diagram text and marking planned work."""

    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.stack, self.designed = [], []
        self.title, self.lede, self.code = "", "", []
        self.sections = [{"title": "Overview", "status": "built", "text": []}]
        self.heading = None
        self.in_lede = self.in_sources = False

    def handle_starttag(self, tag, attrs):
        cls = (dict(attrs).get("class") or "").split()
        if tag in VOID:
            return
        designed = "designed" in cls or "designed-node" in cls
        self.stack.append((tag, designed, "lede" in cls, "sources" in cls))
        if tag == "section":
            self.sections.append({"title": "", "status": "designed" if designed else "built", "text": []})
        elif tag == "h2":
            self.heading = []
        elif designed and not any(s[1] for s in self.stack[:-1]):
            self.sections[-1]["text"].append("[planned] ")
        if tag in BLOCK:
            self.sections[-1]["text"].append("\n")
        elif tag == "small":
            self.sections[-1]["text"].append(": ")
        elif tag == "td":
            self.sections[-1]["text"].append(" | ")
        self.in_lede = self.in_lede or "lede" in cls
        self.in_sources = self.in_sources or "sources" in cls

    def handle_endtag(self, tag):
        if tag in VOID or not any(s[0] == tag for s in self.stack):
            return
        while True:
            name, _, lede, sources = self.stack.pop()
            self.in_lede = self.in_lede and not lede
            self.in_sources = self.in_sources and not sources
            if name == tag:
                break
        if tag == "h2" and self.heading is not None:
            self.sections[-1]["title"] = " ".join("".join(self.heading).split())
            self.heading = None
        if tag in BLOCK:
            self.sections[-1]["text"].append("\n")

    def handle_data(self, data):
        names = {s[0] for s in self.stack}
        if names & {"title"}:
            self.title += data
        if names & SKIP:
            return
        if self.in_sources:
            if "code" in names:
                self.code.append(data.strip())
            return
        if self.in_lede:
            self.lede += data
        if self.heading is not None:
            self.heading.append(data)
            return
        self.sections[-1]["text"].append(data)


def planned_pages():
    nav = (SITE / "site.js").read_text(encoding="utf-8")
    match = re.search(r"\['Planned', \[(.*?)\]\],", nav, re.S)
    return set(re.findall(r"\['([\w-]+)',", match.group(1))) - {"roadmap"} if match else set()


def read_page(path):
    page = Page()
    page.feed(path.read_text(encoding="utf-8"))
    sections = []
    for section in page.sections:
        text = re.sub(r"[ \t]+", " ", "".join(section["text"]))
        text = re.sub(r"\s*\n\s*", "\n", text).strip()
        if text:
            sections.append({"title": section["title"], "id": slug(section["title"]), "status": section["status"], "text": text})
    return {
        "page": f"docs/site/{path.name}",
        "title": page.title.split("·")[0].strip(),
        "summary": " ".join(page.lede.split()),
        "status": "planned" if path.stem in planned_pages() else "built",
        "code": sorted(set(page.code)),
        "sections": sections,
    }


def read_site():
    return [read_page(path) for path in sorted(SITE.glob("*.html"))]


def build_map():
    pages = []
    for page in read_site():
        sections = [{k: s[k] for k in ("id", "title", "status")} | {"words": len(s["text"].split())} for s in page["sections"]]
        pages.append({k: page[k] for k in ("page", "title", "summary", "status", "code")} | {"sections": sections})
    return json.dumps({"pages": pages}, indent=1, ensure_ascii=False) + "\n"


def git(*args):
    return subprocess.run(["git", *args], cwd=ROOT, check=True, capture_output=True, text=True).stdout.strip()


def reviews():
    return json.loads(REVIEWED.read_text(encoding="utf-8")) if REVIEWED.exists() else {}


def stale_pages(pages=None):
    """Returns {page: [code files changed since its last review]}. Unknown commits count as stale."""
    found, reviewed = {}, reviews()
    for page in pages or read_site():
        paths = [p for p in page["code"] if (ROOT / p).exists()]
        commit = reviewed.get(page["page"])
        if not paths:
            continue
        if not commit:
            found[page["page"]] = ["no review recorded"]
            continue
        changed = git("diff", "--name-only", commit, "--", *paths).split()
        if changed:
            found[page["page"]] = changed
    return found


def stale_note(page, stale):
    if "stale" not in FEATURES or page["page"] not in stale:
        return ""
    note = f"STALE: code changed since this page was reviewed ({', '.join(stale[page['page']])}). The code decides what's true, so these lines override the text below:\n"
    commit = reviews().get(page["page"])
    if commit:
        diff = git("diff", "-U0", commit, "--", *[p for p in page["code"] if (ROOT / p).exists()]).splitlines()
        changed = [line for line in diff if re.match(r"[-+](?![-+])", line)]
        note += "".join(line[:160] + "\n" for line in changed[:STALE_LINES])
    return note


def describes(page, path):
    return any(path == code or path.startswith(code.rstrip("/") + "/") or code.startswith(path.rstrip("/") + "/") for code in page["code"])


def find(query):
    words = [w for w in re.findall(r"[\w-]+", query.lower()) if w not in STOP]
    if not words:
        print("Give at least one search word.")
        return 2
    pages, hits = read_site(), []
    for page in pages:
        for section in page["sections"]:
            title, text = section["title"].lower() + " " + page["title"].lower(), section["text"].lower()
            matched = [w for w in set(words) if w in text or w in title]
            score = len(matched) * 10 + sum(text.count(w) for w in matched) + sum(5 for w in matched if w in title)
            if matched:
                hits.append((score, page, section))
    hits.sort(key=lambda hit: -hit[0])
    if not hits:
        print("No section matches. Try other words, or read docs/agent-map.json.")
        return 1
    stale = stale_pages(pages) if "stale" in FEATURES else {}
    for _, page, section in hits[:FIND_LIMIT]:
        planned = " (planned)" if page["status"] == "planned" or section["status"] == "designed" else ""
        print(f"## {page['page']}#{section['id']} {page['title']}: {section['title']}{planned}")
        print(stale_note(page, stale), end="")
        print(section["text"][:FIND_CHARS])
        if page["code"]:
            print("Code: " + ", ".join(page["code"]))
        print()
    return 0


def code(path):
    path = path.removeprefix("./")
    pages = [page for page in read_site() if describes(page, path)]
    if not pages:
        print(f"No page lists {path} as a source. Try find with a word from the file's name.")
        return 1
    stale = stale_pages(pages) if "stale" in FEATURES else {}
    for page in pages:
        print(f"{page['page']}: {page['title']}. {page['summary']}")
        print(stale_note(page, stale), end="")
    return 0


def main(argv):
    command = argv[0] if argv else ""
    if command == "find" and "find" in FEATURES and argv[1:]:
        return find(" ".join(argv[1:]))
    if command == "code" and "code" in FEATURES and len(argv) == 2:
        return code(argv[1])
    if command == "stale" and "stale" in FEATURES:
        found = stale_pages()
        for page, files in found.items():
            print(f"{page}: {', '.join(files)}")
        print(f"{len(found)} stale pages." if found else "No stale pages.")
        return 0
    if command == "mark" and argv[1:]:
        reviewed = reviews()
        for page in argv[1:]:
            if page == "--all":
                reviewed.update({p["page"]: git("rev-parse", "HEAD") for p in read_site()})
            elif not (ROOT / page).exists():
                sys.exit(f"{page}: not found")
            else:
                reviewed[page] = git("rev-parse", "HEAD")
        REVIEWED.write_text(json.dumps(dict(sorted(reviewed.items())), indent=1) + "\n", encoding="utf-8")
        return 0
    if command == "build":
        MAP.write_text(build_map(), encoding="utf-8")
        return 0
    if command == "check":
        if not MAP.exists() or MAP.read_text(encoding="utf-8") != build_map():
            print("docs/agent-map.json is out of date. Run: python3 docs/agent_map.py build")
            return 1
        for page, files in stale_pages().items():
            print(f"WARN {page} may be stale: {', '.join(files)}")
        return 0
    print(__doc__)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
