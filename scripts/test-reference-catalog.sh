#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
docker run --rm -v "$PWD/internal/parity/catalog:/catalog:ro" mcr.microsoft.com/dotnet/sdk:10.0.400 bash -ec '
  mkdir /work
  cp /catalog/Directory.Build.props /work/
  for project in /catalog/*/Reference.csproj; do
    dir=$(dirname "$project")
    target=/work/$(basename "$dir")
    mkdir "$target"
    cp "$project" "$dir/packages.lock.json" "$target/"
    dotnet restore "$target/Reference.csproj" --locked-mode -warnaserror
  done'
for provider in memory sqlite; do
  image=$(docker build -q --build-arg "provider=$provider" -f internal/parity/catalog/Dockerfile .)
  docker run --rm "$image"
done
