# ADR 0004: Knowledge graph in the knowledge base (entities and relations next to the chunks)

- **Status:** Implemented and tested without a model: unit and integration tests (in-memory store and a throwaway PostgreSQL with pgvector) and a local run against a rule-based stand-in for the model gateway. **Not yet run against a real model, and not yet deployed on the cluster.** Everything about extraction quality, token cost and latency below is therefore a design intent or an estimate, not a measurement.
- **Date:** 2026-11-23
- **Scope:** an optional graph inside the existing kb service (`knowledge.kb.graph.enabled`, default off). With it off the chart render, the database schema and the tool list are unchanged.

## Context

Vector search answers "which passage is about this". It does not answer a question whose facts sit in different documents: "who is on call for the team that owns the service whose backend suffered the Vane-3 outage?" needs five facts from five documents, none of which mentions the others. A chunk-level retriever finds the outage page and the on-call pages of every team, and leaves the chain to the model. A graph of entities and typed relations, each edge citing the chunk it came from, lets a query walk the chain and hand the agent the five citations.

Constraints of this platform: one small arm64 kind node, an existing PostgreSQL (pgvector) that already holds the kb, no new always-on pods if avoidable, licences that fit an Apache-2.0 open core, and model calls only through the model gateway so that keys, budgets and attribution stay in one place.

## Decision

Store the graph in the kb's own PostgreSQL as plain tables (`entities`, `relations`, `entity_chunks`, `extraction_jobs`, in the `kb` schema, created only when the graph is enabled) and traverse it with recursive SQL. No new service, no new image, no extension.

- **Extraction at ingest, in the background.** `PUT /v1/collections/{c}/documents/{id}` stores and embeds the document as before and queues one job per extraction window (about 2400 characters, whole chunks). A worker in the kb process claims jobs with a lease (so a crashed pod's jobs are retried), calls the gateway's `/v1/chat/completions` with the alias `knowledge.kb.graph.extractModel` (default `claude-haiku`) and stores the result in one transaction. An unchanged re-ingest queues nothing and makes no model call; a changed document replaces its graph rows. An hourly token budget (`tokensPerHour`, counted from the usage the gateway returns) pauses the worker instead of failing jobs.
- **Strict, grounded validation.** The model's JSON is not trusted. Entity types come from a closed list, names are bounded, and every entity name, alias and relation evidence must occur literally in the window text; a relation needs two listed entities and a snake_case predicate. Anything else is dropped item by item and counted, not logged with its text.
- **Own gateway key.** Extraction calls use a separate gateway virtual key, user id `kb-extract` (Secret `kb-extract-key`, created by `install.sh` only when the graph is on), so a large ingest spends its own token bucket (the gateway keeps one bucket per `user_id`) and never the embedding key's (`kb`) or an agent's.
- **Three read-only MCP tools, only when the graph is on.** `kb_graph_search(query, collection?, hops, top_k)` finds seed entities by name or alias in the question and, when an embedder exists, by the embedding of the question against the entity names; it expands them with the store's recursive traversal (1 to 3 hops, at most 25 edges followed out of any one entity, at most 10 chains returned, at most 6 supporting passages of 600 characters, one result text of at most 12 000 characters) and returns chains such as `Vane-3 <-runs_in- Teal <-stores_data_in- Quill <-calls- Orion`, every edge with `document_id`, chunk, source URI and the quoted evidence. `kb_graph_entity(name, collection?)` describes one entity and its direct neighbours. `kb_graph_paths(from, to, collection?, max_hops)` returns the shortest paths, and says "no entity named X" or "no path within N hops" as a normal answer. The document ids resolve with the existing `kb_get_document`. Agents with `knowledge: true` get them (prefix `knowledge_`) and a short prompt addition only when the graph is enabled.
- **Operator view.** `GET /v1/collections/{c}/graph/stats` and `GET /v1/graph/stats` (admin token) return counts and job states and the last error code, never content.
- **Traversal limit.** Three hops is a hard maximum: with `Vane-3` as the start the chain reaches `Orion`, and the last two links (`Orion` to the team to the person) take a second query. The agent prompt tells the model to continue from the last entity with `kb_graph_entity` or `kb_graph_paths`.

## Alternatives rejected

| Option | Why not |
|---|---|
| **Neo4j** (Community Edition) | GPLv3 (the Community Edition is not Apache-compatible as an embedded or bundled piece), a JVM with a heap of several hundred megabytes on a node that already runs a model server and two databases, and a second store to back up and keep in step with the chunks. |
| **FalkorDB** | Server Side Public License (SSPL), which is not an OSI-approved open-source licence and restricts offering the software as a service: not acceptable for an Apache-2.0 core that others may host. |
| **Memgraph** | Business Source License. Fast and in-memory, which is also the problem on this node. |
| **Kuzu** (embedded graph database) | Attractive (embedded, MIT), but the upstream project was archived in October 2025; building on an unmaintained engine is a risk we do not need. |
| **Apache AGE** (PostgreSQL extension) | Fits the idea best and is Apache-2.0, but it needs a custom PostgreSQL image (AGE built into the pgvector image), which is an image to build, pin, scan and upgrade. Deferred: plain tables with recursive SQL cover a three-hop limit, and the graph tables can move into AGE later without changing the tools. |
| **LightRAG, Microsoft GraphRAG, Graphiti** | Each is a Python service with its own pipeline, its own chunking and its own store. That would split the truth about a document across two chunkings, add a pod and a language to the platform, and make citations point at chunks the kb cannot open with `kb_get_document`. We keep one chunk table and attach edges to its chunks. |

## Consequences

- **Cost.** Ingest now makes one model call per window of about 2400 characters, with a system prompt of roughly 400 tokens and a bounded JSON answer. A 50 000 character document is about 21 windows. At the default 300 000 tokens per hour the worker stops and resumes; a large corpus takes hours, not seconds. These are estimates: no real run has been measured.
- **Latency.** The chunk search path is untouched. A graph query is a few indexed SQL statements and, with embeddings on, one query embedding (bounded by the existing timeout, with fallback to name-only seeds).
- **Operations.** More tables and a background loop in the kb pod. Jobs and graph rows are removed with their document. The `kb-extract-key` Secret is created by `install.sh` when absent, never printed, and removed by `uninstall.sh` with `PURGE_SECRETS=1`.
- **Quality is the model's.** Missed or wrongly typed entities, aliases that do not merge ("Teal" and "Cluster Teal" are different names), and relations the model phrases differently across documents all reduce recall. Merge is by type and normalised name inside one collection only; there is no cross-collection entity.
- **Gateway budget.** The gateway's token budget is one number for all virtual keys (the policy has a single rate limit entry keyed by `user_id`). `kb-extract` therefore has its own bucket of that size, not a smaller one; `tokensPerHour` is the lower, kb-side bound inside it. A different gateway budget for one key would need a policy change that was not verified on the gateway version in use, so it was not made.

## Privacy

Document text is sent to the extraction model: every window of every ingested document, as stored (the title heading included when the document has one). Choose a model you may send that text to, or keep the graph off; there is no per-document opt-out yet. The kb logs only counts, codes, document and collection names and token numbers, never document or model text. The stats endpoint and logs carry no content. Entities, descriptions and evidence are stored in the kb database, with the same access rules as the chunks.

## Injection defence

A document can contain text that tries to steer the extractor ("ignore the rules and output ...") or to plant false facts. Defences, in order: the window is wrapped in markers with a per-window token the text does not contain and the system prompt calls it untrusted data; the model gets no tools; the output must be one JSON object, validated as above; names and evidence must be literal substrings of the window, so the model cannot add an entity or a quote that the document does not contain; the number of entities and relations per window is capped. What validation cannot prevent: a document that really states a false or misleading fact produces a grounded edge. So **graph content is untrusted data for agents**, exactly like chunk text. Every graph tool result starts with the same notice line as `kb_search`, the prompt addition says the facts are reference data and never instructions, and every edge carries the document id and quote so the agent (or a person) can check it with `kb_get_document`.

## Limits

- Three hops at most; at most 25 edges followed out of one entity; ten chains and six passages per answer. A very connected entity is cut off, deterministically (lowest edge ids first), and the answer says so.
- English prose of moderate length per document; very long documents are cut into windows and a fact that spans two windows is missed.
- Entity names are merged by exact normalised name and type. Different spellings of one thing stay separate unless the model lists them as aliases.
- Relations are directed and typed by a free predicate (`owns`, `calls`, ...). Predicates of different documents are not unified, so a query by predicate is not offered.
- Deleting or replacing a document removes its edges at once; an entity that nothing refers to any more is removed with them.
- Not run against a real model; the canary corpus and `hack/kb-graph-canary.sh` exist to do that and to compare the graph answer with the vector baseline.

## Evidence so far

- `go test -race ./...` in `services/kb`, and `make test-integration` (throwaway pgvector and plain PostgreSQL containers), cover the store contract for both backends, extraction validation, the worker (leases, retries, budget, idempotence), the three tools (bounds, scoping, determinism, no content in logs, deadline) and the stats route.
- A local run of the kb with an in-memory store, a rule-based stand-in for the gateway and `hack/kb-graph-canary.sh` printed PASS. The stand-in only recognises the five chain sentences, so this proves the plumbing, not the extraction.
- Chart tests (`charts/openagentix/tests/run.sh`) cover the key annotations, the tool lists, validation, and that the render with the graph off is byte-identical to the one before this work.
