#!/usr/bin/env python3
"""Check repository Markdown links, the AI index, and the complete AI quickstart."""
import os
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


def run_ai_example(target, cwd, env):
    output = subprocess.check_output(["go", "run", target], cwd=cwd, env=env, text=True, timeout=120)
    if output != "guide 0 0\n":
        raise SystemExit(f"unexpected AI quickstart output in {cwd}: {output!r}")


def check_ai_quickstart(guide):
    setup_section = guide.partition("## Version and starting point\n")[2].split("\n## ", 1)[0]
    release = re.search(r"\bpublished \*\*(v\d+\.\d+\.\d+(?:-[\w.-]+)?)\*\*", setup_section)
    blocks = re.findall(r"^```sh\n(.*?)^```$", setup_section, flags=re.M | re.S)
    if not release or len(blocks) != 1:
        raise SystemExit("AI guide must name its published release and include one setup block")
    commands = blocks[0].splitlines()
    install = f"go get github.com/gabisonia/go-vectorstore/stores/memory@{release[1]}"
    if commands != ["go mod init example.com/vector-demo", install]:
        raise SystemExit(f"expected module initialization and published install command: {install}")
    programs = re.findall(r"^```go\n(package main\n.*?)^```", guide, flags=re.M | re.S)
    if len(programs) != 1:
        raise SystemExit("expected exactly one complete AI quickstart")
    with tempfile.TemporaryDirectory(prefix="vectorstore-docs-") as directory:
        source = Path(directory) / "main.go"
        source.write_text(programs[0])
        env = {**os.environ, "GOWORK": "off"}
        run_ai_example(str(source), ROOT, env)
        for command in commands:
            subprocess.run(command.split(), cwd=directory, env=env, check=True, timeout=300)
        run_ai_example(".", directory, env)


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
    check_ai_quickstart(guide)
    print(f"Checked {len(files)} documentation files and ran the AI quickstart against the checkout and its published pin.")


if __name__ == "__main__":
    main()
