# Knowledge base

The OpenAgentix knowledge base canary code is HERON-4412-KB-RETRIEVED.

## What it is

A small service that stores documents, splits them into chunks and answers searches. Agents use it through three read-only MCP tools: search, get document and list collections. Documents go in through a separate admin API that needs a bearer token.

## Search

Every search combines two rankings. Lexical search matches the words of the question against the text. Vector search compares the meaning of the question with the meaning of each chunk, using an embedding model that runs on the CPU inside the cluster. The two rankings are merged with reciprocal rank fusion, so a chunk that says the right thing in different words still comes up.

## Storage

Chunks and their vectors live in PostgreSQL with the pgvector extension. The database has its own persistent volume and its own generated password. Nothing else in the cluster can open a connection to it.

## Changing the embedding model

Vectors from different models cannot be compared. After you change the model, or its dimension, you must ingest the documents again.

## Ingesting your own documents

Run hack/kb-ingest.sh with a directory of markdown files and a collection name. The document id comes from the file path and the title from the first heading, so running it twice leaves one copy of each document.
