#!/bin/sh
set -eu
ollama show "$SOURCE_CHAT_MODEL" >/dev/null 2>&1 || ollama pull "$SOURCE_CHAT_MODEL"
ollama show "$EMBED_MODEL" >/dev/null 2>&1 || ollama pull "$EMBED_MODEL"
printf 'FROM %s\nPARAMETER num_thread %s\nPARAMETER num_ctx 4096\nPARAMETER num_predict 256\n' "$SOURCE_CHAT_MODEL" "$MODEL_THREADS" > /tmp/demo.Modelfile
ollama create doc-agent-chat -f /tmp/demo.Modelfile
