#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
if [[ ${CI:-} == true || ${GITHUB_ACTIONS:-} == true ]]; then
  echo 'Skipping live Azure AI Search interoperability in CI.'
  exit 0
fi
service=${AZURE_SEARCH_SERVICE:-}
if [[ -z "$service" ]]; then
  services=$(az resource list --resource-type Microsoft.Search/searchServices --query '[].name' --output tsv)
  if [[ -z "$services" || "$services" == *$'\n'* ]]; then
    echo 'Set AZURE_SEARCH_SERVICE to one existing search service in the selected subscription.' >&2
    exit 1
  fi
  service=$services
fi
runner=$(docker build -q --build-arg provider=azure-search -f internal/parity/catalog/Dockerfile .)
az account get-access-token --scope https://search.azure.com/.default --query accessToken -o tsv |
  docker run --rm -i -e AZURE_SEARCH_SERVICE="$service" "$runner"
