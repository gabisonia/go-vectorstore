# About this demo

## Agent and retrieval responsibilities

Microsoft Agent Framework Go runs the conversation and automatic tool-calling
loop. The model chooses a search query and which section to inspect. The
search_docs tool retrieves indexed passages through go-vectorstore. The
read_section tool reads a complete indexed section by its returned identifier.
The assistant must inspect a section before citing it in its answer.

A basic retrieval-then-generation program would not need an agent framework.
The framework is useful here for tool schemas, tool dispatch, repeated model/tool
turns, and conversation sessions. The vector store supplies durable local
retrieval. A framework is not necessary merely to send a prompt to a model.

## Local execution

Ollama serves the chat and embedding models locally. SQLite holds the document
index. The first run downloads container images and model weights; inference
needs no paid API or cloud account. Containers use CPU by default. Answers and
tool choices can vary with model size and hardware. This is a learning demo, not
a production authorization or answer-verification system.
