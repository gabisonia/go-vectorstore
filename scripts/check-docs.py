#!/usr/bin/env python3
"""Check repository Markdown links, the AI index, and the complete AI quickstart."""
from pathlib import Path
import re
import subprocess
import tempfile
from urllib.parse import unquote

ROOT = Path(__file__).resolve().parents[1]
RAW = "https://raw.githubusercontent.com/gabisonia/go-vectorstore/master/"


def without_fences(text):
    return re.sub(r"^```[^\n]*\n.*?^```[^\n]*$", "", text, flags=re.M | re.S)


def anchors(path):
    counts, result = {}, set()
    for heading in re.findall(r"^#{1,6}\s+(.+?)\s*#*\s*$", without_fences(path.read_text()), re.M):
        slug = re.sub(r"[^\w\- ]", "", heading.lower()).replace(" ", "-")
        count = counts.get(slug, 0)
        counts[slug] = count + 1
        result.add(slug + (f"-{count}" if count else ""))
    return result


def main():
    files = list(ROOT.glob("*.md")) + list((ROOT / "docs").rglob("*.md")) + list((ROOT / "samples").rglob("*.md")) + [ROOT / "llms.txt"]
    errors = []
    for path in files:
        for url in re.findall(r"\]\(([^\s)]+)\)", without_fences(path.read_text())):
            if url.startswith(RAW):
                target = ROOT / unquote(url[len(RAW):])
            elif re.match(r"[a-zA-Z][a-zA-Z\d+.-]*:", url):
                continue
            else:
                target = path.parent / unquote(url.split("#", 1)[0]) if not url.startswith("#") else path
            anchor = unquote(url.partition("#")[2])
            if not target.is_file():
                errors.append(f"{path.relative_to(ROOT)}: missing {url}")
            elif anchor and target.suffix == ".md" and anchor not in anchors(target):
                errors.append(f"{path.relative_to(ROOT)}: missing anchor {url}")
    version = "v" + (ROOT / "VERSION").read_text().strip()
    for name in ["llms.txt", "docs/ai-guide.md"]:
        if version not in (ROOT / name).read_text():
            errors.append(f"{name}: missing current VERSION {version}")
    if errors:
        raise SystemExit("\n".join(errors))
    guide = (ROOT / "docs/ai-guide.md").read_text()
    programs = re.findall(r"^```go\n(package main\n.*?)^```", guide, flags=re.M | re.S)
    if len(programs) != 1:
        raise SystemExit("expected exactly one complete AI quickstart")
    with tempfile.TemporaryDirectory(prefix="vectorstore-docs-") as directory:
        source = Path(directory) / "main.go"
        source.write_text(programs[0])
        result = subprocess.run(["go", "run", str(source)], cwd=ROOT, capture_output=True, text=True, timeout=120, check=True)
        if result.stdout != "guide 0 0\n":
            raise SystemExit(f"unexpected AI quickstart output: {result.stdout!r}")
    print(f"Checked {len(files)} documentation files and executed the AI quickstart.")


if __name__ == "__main__":
    main()
