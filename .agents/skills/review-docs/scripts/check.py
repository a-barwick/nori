#!/usr/bin/env python3
"""Checks docs site pages for what a script can catch. Usage: check.py PAGE [PAGE ...] or --all"""
import re
import sys
from html.parser import HTMLParser
from pathlib import Path

SITE = Path(__file__).resolve().parents[4] / "docs" / "site"
WORD_LIMIT = 800
WORD_TARGET = 600
LONG_PAGES = {"scenarios", "decisions", "reading", "sources", "words", "code", "open-questions"}
VISUALS = {"table", "svg", "img", "pre"}
VISUAL_CLASSES = {"flow", "states", "shots", "grid2", "grid3", "map"}
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
PRAISE = r"\b(?:real|really|actual|actually|honest|honestly|true|truly|trust\w*|natural\w*|feels?|useful|meaningful\w*|powerful)\b"
SHORTHAND = r"\s(?:=|→|\+|≠)\s"


class Page(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.stack, self.text, self.prose, self.classes, self.links, self.order = [], [], [], set(), [], []
        self.title, self.heading_links, self.scripts, self.has_style = "", [], [], False
        self.block = None

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        cls = (attrs.get("class") or "").split()
        self.classes.update(cls)
        self.order.append((tag, cls))
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
        if tag in ("p", "li") and self.block is not None and tag not in self.stack:
            if not {"p", "li"} & set(self.stack):
                self.prose.append("".join(self.block).strip())
                self.block = None

    def handle_data(self, data):
        if "head" in self.stack:
            if "title" in self.stack:
                self.title += data
            return
        if RAW & set(self.stack):
            if self.block is not None and "code" in self.stack:
                self.block.append("`code`")
            return
        self.text.append(data)
        if self.block is not None:
            self.block.append(data)


def known_classes():
    css = (SITE / "site.css").read_text(encoding="utf-8")
    js = (SITE / "site.js").read_text(encoding="utf-8")
    return set(re.findall(r"\.([a-zA-Z][\w-]*)", css)) | set(re.findall(r"class(?:Name)?=?[\"' ]+([\w-]+)", js))


def visible_words(page):
    return len(" ".join(page.text).split())


def check(path):
    fails, warns = [], []
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

    # A visual must open the page: before the first section or at the top of it
    sections = [i for i, (tag, _) in enumerate(page.order) if tag == "section"]
    opening = page.order[:sections[1] if len(sections) > 1 else len(page.order)]
    if not any(tag in VISUALS or VISUAL_CLASSES & set(cls) for tag, cls in opening):
        fails.append("no visual before the second section")

    words = visible_words(page)
    for box in re.findall(r'class="node[^"]*">(.*?)</div>', source, re.S):
        text = re.sub(r"<[^>]+>", " ", box)
        if len(text.split()) > 28:
            warns.append(f"long diagram box, move detail below: {text.split()[0]} ...")
    if words > WORD_LIMIT and name not in LONG_PAGES:
        fails.append(f"{words} words, over the {WORD_LIMIT} limit: split it by topic")
    elif words > WORD_TARGET and name not in LONG_PAGES:
        warns.append(f"{words} words, over the {WORD_TARGET} target: cut what a reader can't act on")
    if [cls for tag, cls in page.order if tag == "details"].count(["more"]) > 1:
        warns.append("more than one details.more section")

    if page.heading_links:
        fails.append("heading contains a link: only the copy icon is interactive")
    for src in page.scripts:
        if src.startswith(("http:", "https:", "//")):
            fails.append(f"external script: {src}")
    if page.has_style:
        warns.append("inline <style>: use site.css classes")
    unknown = sorted(page.classes - known_classes())
    if unknown:
        fails.append(f"classes not in site.css or site.js: {', '.join(unknown)}")

    for link in page.links:
        target = link.split("#")[0]
        if link.startswith("/"):
            fails.append(f"use a relative link: {link}")
        elif target and not re.match(r"[a-z]+:", target) and not (SITE / target).exists():
            fails.append(f"broken link: {link}")

    if re.search(r"[\w.+-]+@[\w-]+\.\w+|\bsk-[A-Za-z0-9]{8,}", source):
        fails.append("possible email address or API key")

    for block in page.prose:
        short = block if len(block) < 90 else block[:87] + "..."
        for pattern, label in PHRASES:
            if re.search(pattern, block, re.I):
                warns.append(f"{label}: {short}")
        for match in re.findall(PRAISE, block, re.I):
            warns.append(f"check '{match}' states a fact: {short}")
        if re.search(SHORTHAND, block):
            warns.append(f"symbol used as shorthand: {short}")
        if len(re.findall(r"[.!?](?:\s|$)", block)) > 3:
            warns.append(f"long paragraph, use a list or split it: {short}")
    return words, fails, warns


def main(argv):
    if not argv:
        print(__doc__)
        return 2
    paths = sorted(SITE.glob("*.html")) if argv == ["--all"] else [Path(a) for a in argv]
    failed = False
    for path in paths:
        if not path.exists():
            print(f"{path}: not found")
            failed = True
            continue
        words, fails, warns = check(path.resolve())
        print(f"{path.name}: {words} words, {len(fails)} fail, {len(warns)} warn")
        for line in fails:
            print(f"  FAIL {line}")
        for line in warns:
            print(f"  WARN {line}")
        failed = failed or bool(fails)
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
