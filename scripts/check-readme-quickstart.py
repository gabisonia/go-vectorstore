"""Run the published SQLite quickstart as a consumer outside the checkout."""
import os
from pathlib import Path
import re
import subprocess
import tempfile

readme = (Path(__file__).resolve().parents[1] / "README.md").read_text()
section = readme.split("## Your first SQLite search\n", 1)[1].split("\n## ", 1)[0]


def block(language):
    (content,) = re.findall(rf"^```{language}\n(.*?)^```$", section, re.M | re.S)
    return content


setup = block("sh").splitlines()
if len(setup) != 2 or setup[0] != "go mod init example.com/handbook" or not re.fullmatch(
    r"go get github\.com/gabisonia/go-vectorstore/stores/sqlite@v\d+\.\d+\.\d+", setup[1]
):
    raise SystemExit("expected module initialization and a pinned SQLite release")
with tempfile.TemporaryDirectory(prefix="readme-sqlite-") as directory:
    env = {**os.environ, "GOWORK": "off", "CGO_ENABLED": "0"}
    for command in setup:
        subprocess.run(command.split(), cwd=directory, env=env, check=True, timeout=300)
    (Path(directory) / "main.go").write_text(block("go"))
    for attempt in range(2):
        output = subprocess.check_output(
            ["go", "run", "."], cwd=directory, env=env, text=True, timeout=300
        )
        if output != block("text"):
            raise SystemExit(f"run {attempt + 1}: unexpected result {output!r}")
print("README quickstart passed on a new and reopened SQLite database.")
