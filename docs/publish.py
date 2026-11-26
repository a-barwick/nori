#!/usr/bin/env python3
import html
import re
import shutil
import sys
from html.parser import HTMLParser
from itertools import accumulate
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SITE = ROOT / "docs" / "site"
SKIP = {"script", "style", "head", "defs"}
VOID = {"br", "hr", "img", "input", "link", "meta", "path", "circle", "rect", "line", "polygon", "use", "ellipse"}
BLOCK = {"p", "li", "tr", "h1", "h2", "h3", "div", "section", "details", "summary", "text", "pre", "ul", "table"}
REPO_ONLY = {"sources", "code"}
INDEXED = False


class Page(HTMLParser):
    def __init__(self, source):
        super().__init__(convert_charrefs=True)
        self.source = source
        self.line_offsets = list(accumulate((len(line) + 1 for line in source.split("\n")), initial=0))
        self.private_start = None
        self.private_sections = []
        self.stack = []
        self.title, self.lede = "", ""
        self.sections = [{"title": "Overview", "text": []}]
        self.heading = None
        self.in_lede = self.in_sources = False
        self.feed(source)
        self.close()
        if self.in_sources:
            raise ValueError("Unclosed private source section")

    def source_offset(self):
        line, column = self.getpos()
        return self.line_offsets[line - 1] + column

    def handle_starttag(self, tag, attrs):
        cls = (dict(attrs).get("class") or "").split()
        if tag in VOID:
            return
        if "sources" in cls and not self.in_sources:
            self.private_start = self.source_offset()
        designed = "designed" in cls or "designed-node" in cls
        self.stack.append((tag, designed, "lede" in cls, "sources" in cls))
        if tag == "section":
            self.sections.append({"title": "", "text": []})
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
        was_private = self.in_sources
        while True:
            name, _, lede, sources = self.stack.pop()
            self.in_lede = self.in_lede and not lede
            self.in_sources = self.in_sources and not sources
            if name == tag:
                break
        self.in_sources = any(s[3] for s in self.stack)
        if was_private and not self.in_sources:
            end = self.source.index(">", self.source_offset()) + 1
            self.private_sections.append((self.private_start, end))
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
            return
        if self.in_lede:
            self.lede += data
        if self.heading is not None:
            self.heading.append(data)
            return
        self.sections[-1]["text"].append(data)

    def public_source(self):
        source = self.source
        for start, end in reversed(self.private_sections):
            source = source[:start] + source[end:]
        return source


def read_page(path):
    page = Page(path.read_text(encoding="utf-8"))
    sections = []
    for section in page.sections:
        text = re.sub(r"[ \t]+", " ", "".join(section["text"]))
        text = re.sub(r"\s*\n\s*", "\n", text).strip()
        if text:
            sections.append({"title": section["title"], "text": text})
    return {
        "path": path,
        "html": page.public_source(),
        "title": page.title.split("·")[0].strip(),
        "summary": " ".join(page.lede.split()),
        "sections": sections,
    }


def public_html(page, nav):
    path, text = page["path"], page["html"]
    for name in REPO_ONLY:
        text = re.sub(rf'<a [^>]*href="{name}\.html[^"]*"[^>]*>(.*?)</a>', r"\1", text, flags=re.S)
    used = set()

    def heading(match):
        title = re.sub(r"<[^>]+>", "", match.group(3))
        anchor = re.sub(r"[^a-z0-9]+", "-", html.unescape(title).lower().replace("&", " and ")).strip("-")
        if anchor in used or match.group(2):
            return match.group(0)
        used.add(anchor)
        return f'<{match.group(1)} id="{anchor}"{match.group(2)}>{match.group(3)}</{match.group(1)}>'

    text = re.sub(r"<(h[23])((?: [^>]*)?)>(.*?)</\1>", heading, text, flags=re.S)
    robots = "" if INDEXED else '<meta name="robots" content="noindex">\n'
    head = f'<meta name="description" content="{html.escape(page["summary"], quote=True)}">\n{robots}<link rel="alternate" type="text/markdown" href="{path.stem}.md">\n'
    text = text.replace("</head>", head + "</head>", 1)
    return text.replace("<body>\n", "<body>\n" + nav, 1)


def publish(out):
    if out.exists() and any(out.iterdir()):
        raise ValueError("The public output directory must be empty")
    shutil.copytree(SITE, out, dirs_exist_ok=True, ignore=shutil.ignore_patterns("*.html", "AGENTS.md"))
    pages = [read_page(path) for path in sorted(SITE.glob("*.html")) if path.stem not in REPO_ONLY]
    links = "".join(f'<li><a tabindex="-1" href="{p["path"].name}">{html.escape(p["title"])}</a>: {html.escape(p["summary"])} <a tabindex="-1" href="{p["path"].stem}.md">Plain text</a></li>' for p in pages)
    nav = f'<nav aria-hidden="true" style="position:absolute;width:1px;height:1px;overflow:hidden;clip-path:inset(50%)"><p>Pages on this site. <a tabindex="-1" href="llms.txt">Plain text index</a></p><ul>{links}</ul></nav>\n'
    for page in pages:
        path = page["path"]
        rendered = public_html(page, nav)
        (out / path.name).write_text(rendered, encoding="utf-8")
        if path.stem in ("privacy", "terms"):
            clean_dir = out / path.stem
            clean_dir.mkdir(exist_ok=True)
            (clean_dir / "index.html").write_text(rendered, encoding="utf-8")
        body = "\n\n".join((f"## {s['title']}\n\n" if s["title"] != "Overview" else "") + s["text"] for s in page["sections"])
        (out / f"{path.stem}.md").write_text(body + "\n", encoding="utf-8")
    lines = ["# Nori Dokyu", "", "> How the Nori assistant engine works. Each page below has a plain text copy.", ""]
    lines += [f"- [{p['title']}]({p['path'].stem}.md): {p['summary']}" for p in pages]
    (out / "llms.txt").write_text("\n".join(lines) + "\n", encoding="utf-8")
    robots_txt = "User-agent: *\nAllow: /\n" if INDEXED else "User-agent: *\nDisallow: /\n"
    (out / "robots.txt").write_text(robots_txt, encoding="utf-8")
    for name in ("request-data.js", "task-data.js"):
        data = out / name
        data.write_text(re.sub(r"'(?:core|web|docs|integrations)/[^']*'", "''", re.sub(r"\bsource: '[^']*'", "source: ''", data.read_text(encoding="utf-8"))), encoding="utf-8")
    script = out / "site-config.js"
    source = script.read_text(encoding="utf-8")
    for name in REPO_ONLY:
        source = re.sub(rf",?\s*\['{name}', '[^']*'\]", "", source, count=1)
    script.write_text(source, encoding="utf-8")


if __name__ == "__main__":
    if len(sys.argv) != 2:
        sys.exit("Usage: python3 docs/publish.py DIR")
    publish(Path(sys.argv[1]))
