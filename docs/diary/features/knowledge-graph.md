---
title: Knowledge graph
sidebar_position: 4.5
description: Optional entity and relation graph inside the knowledge base, for questions that need facts from several documents.
---

# Knowledge graph

## What it is for

Vector search finds passages that look like the question. It cannot join facts that sit in different documents. Example: one document says which cluster had an outage, another which API stores data there, a third which service calls that API, a fourth who owns the service, a fifth who is on call. No single passage holds the answer.

The knowledge graph stores the links between named things, so an agent can walk from one fact to the next and cite each step.

It is part of the knowledge base service (`services/kb`). It adds no pod and no database: the graph lives in plain tables in the knowledge base PostgreSQL. It is off by default.

## How it works

```mermaid
flowchart TD
  A["PUT document"] --> B["Chunk, embed, store (as before)"]
  B --> C["Enqueue extraction jobs"]
  C --> D["Background worker in the kb pod"]
  D --> E["Model gateway, key kb-extract"]
  E --> F["Validate the answer"]
  F --> G["Tables: entities, relations, entity_chunks"]
  H["Agent"] --> I["vMCP tools"]
  I --> J["knowledge_kb_graph_search / entity / paths"]
  J --> G
```

Ingest:

1. `PUT` of a document returns as before. It does not wait for the graph.
2. A worker takes one window of the document (whole chunks, up to 2400 characters) and sends it to the model through the gateway. The request carries a strict JSON schema and no tools.
3. The answer is validated. A row is kept only when:
   - the entity type is one of `service, team, person, system, location, organization, product, event, concept`;
   - the entity name and the evidence sentence literally occur in the window;
   - the name is a real name, not a generic word ("database", "the wiki", "on-call engineer");
   - a person has a proper name, and an event has a date or number in its name.
4. Entities merge by a canonical name: category words at either end do not count, so `Team Basalt` and `Basalt` are one entity. Type is not part of the key. Entities never merge across collections.
5. Every relation stores its document id, chunk index, evidence sentence, model and prompt version.

Jobs are idempotent and resumable. Re-ingesting unchanged content makes no model call. Changing `promptVersion` re-extracts a document at its next ingest. The worker backs off on HTTP 429 and keeps to an hourly token budget.

Query:

```mermaid
sequenceDiagram
  participant Agent
  participant vMCP as vMCP tools
  participant KB as kb service
  participant PG as PostgreSQL
  Agent->>vMCP: knowledge_kb_graph_search(query, collection, hops)
  vMCP->>KB: kb_graph_search
  KB->>PG: find seed entities named in the query
  KB->>PG: recursive SQL, up to 3 hops, fan-out cap 25
  PG-->>KB: paths with citations
  KB-->>Agent: ranked chains, facts with document ids and evidence
  Agent->>vMCP: knowledge_kb_graph_paths(from, to)
  vMCP-->>Agent: shortest path with per-edge citations
```

Tools (read-only, registered only when the graph is on):

| Tool as the agent sees it | Arguments | Result |
|---|---|---|
| `knowledge_kb_graph_search` | `query`, `collection`, `hops` (1 to 3), `top_k` | Chains of facts around the entities named in the query, each fact with document id, chunk and evidence |
| `knowledge_kb_graph_entity` | `name`, `collection` | One entity, its aliases and direct neighbours |
| `knowledge_kb_graph_paths` | `from`, `to`, `collection`, `max_hops` | Shortest paths between two entities |

Chains that span more documents rank first. Answers are bounded: 10 chains, 6 passages, 12000 characters. Every answer starts with a notice that the content is untrusted data.

## How to enable

```yaml
knowledge:
  kb:
    graph:
      enabled: true
      extractModel: claude-haiku     # a gateway model alias
      tokensPerHour: 300000          # kb-side budget for extraction
      windowChars: 2400
      maxHops: 3
      promptVersion: v2
```

`install.sh` creates a separate gateway key for extraction (Secret `kb-extract-key`, gateway user `kb-extract`), so a large ingest cannot use up the agents' budget. Agents with `knowledge: true` get the three tools.

Demo values: `components/knowledge/graph-demo-values.yaml`.

## How to try it

```sh
VALUES_FILE="components/knowledge/graph-demo-values.yaml" ./install.sh
hack/kb-graph-canary.sh
```

The canary ingests 26 short fictional documents into the collection `graph-canary`, waits for extraction, prints the vector-search baseline, runs the graph tools and prints PASS or FAIL. It spends model tokens and sends the corpus text to the extraction model.

Operator status, no content: `GET /v1/collections/<collection>/graph/stats` (entities, relations, jobs pending, done, failed, last error code).

## Curated facts

Extraction is not the only way to put facts in the graph. An operator can write a catalogue by hand (services, teams, owners, dependencies, escalation contacts) and load it with no model call. The agent gets those facts from the same three tools, mixed with the extracted ones, and each curated fact is marked as such.

```mermaid
flowchart LR
  A["Operator file (YAML or JSON)"] --> B["hack/kb-graph-load.sh"]
  B --> C["PUT .../graph/factsets/id"]
  C --> D["Validate, replace the fact set atomically"]
  D --> E["Same tables, origin manual"]
  F["Extraction worker"] --> G["Same tables, origin extracted"]
  E --> H["kb_graph_search / entity / paths"]
  G --> H
```

A **fact set** is a named catalogue of one collection. Loading it replaces the previous version of the same id in one transaction: the same body twice changes nothing, and facts that disappeared from the file disappear from the graph. Other fact sets, and everything extracted from documents, are left alone.

```sh
hack/kb-graph-load.sh graph-canary org-catalogue components/knowledge/graph-sample/org-catalogue.yaml
```

The file has an optional `source_uri` (an http or https URL, shown with each fact), a list of `entities` (`name`, `type`, optional `description` and `aliases`) and a list of `relations` (`source`, `predicate`, `target`, optional `note`). YAML needs `yq` on the machine; JSON does not.

REST, same admin token, body limit and no-content logging as the document routes. They exist only when the graph is on:

| Route | Meaning |
|---|---|
| `PUT /v1/collections/{collection}/graph/factsets/{id}` | Replace the fact set. Answers the counts and whether anything changed |
| `GET /v1/collections/{collection}/graph/factsets/{id}` | The stored body (it is operator data) |
| `GET /v1/collections/{collection}/graph/factsets` | The fact sets of the collection with counts |
| `DELETE /v1/collections/{collection}/graph/factsets/{id}` | Remove the fact set and its relations |

Rules:

- Entity types, the 80 character name limit and the predicate form (`owns`, `depends_on`) are those of extraction. The grounding rules are not: a curated entity needs no occurrence in any text and may have a generic name, because the operator decides.
- Both ends of a relation must be entities declared in the same fact set, by name, alias or canonical name. A fact set means the same whatever else is in the collection.
- At most 2000 entities and 5000 relations per fact set (413 above that). Every other mistake is a 400 that names the item, for example `relations[7]: target is not an entity declared in this fact set`.
- A curated entity merges with an extracted one by the same canonical name. The curated name, type and description win for display, and the entity is marked manual.
- Deleting or re-ingesting documents never removes a curated entity or relation. Deleting a fact set un-marks its entities; one that extracted facts still use stays, and one that nothing refers to is removed.

What the agent sees:

```text
Basalt -escalates_to-> Priya Venkat | curated: factset=org-catalogue source=https://example.com/openagentix/org-catalogue | "Escalations go to Priya Venkat"
```

instead of the `document_id=... chunk=...` of an extracted fact. A curated fact has a note, not a passage, so it adds nothing to the supporting passages. In ranking, a fact set counts as one source. The answer still starts with the notice that its content is untrusted, because it mixes in extracted text.

`GET /v1/collections/{collection}/graph/stats` adds `manual`: counts of curated entities, relations and fact sets.

The sample `components/knowledge/graph-sample/org-catalogue.yaml` goes with the canary corpus and holds none of its answer chain. The question "Which service calls the ledger API Quill, which team owns it, and who is that team's escalation manager?" needs two extracted facts and one curated one (Orion, Basalt, Priya Venkat).

Status: unit and integration tested (in-memory store, throwaway PostgreSQL, the upgrade of an existing schema), and the sample question is tested offline on the stored extraction of the canary run. Proven on the kind demo cluster on 2026-11-23: the sample catalogue was loaded (19 entities, 12 relations) and the agent answered a question that needs two extracted facts and one curated fact, naming the fact set as the source of the curated one.

## What was proven

On the kind demo cluster with a real model (2026-11-23):

- Extraction: 26 of 26 jobs done, 47,073 tokens, 33 entities, 29 relations.
- Canary PASS: `Vane-3 <- Teal <- Quill <- Orion` and `Orion <- Basalt <- Mira Okafor` found, citing the five chain documents.
- The `assistant` agent answered the five-document question correctly through the tools and listed all five documents. It also explained why a look-alike chain in the same region was not the answer.
- Vector search alone returned on-call and ownership documents but could not link them.

Unit and integration tests cover storage on PostgreSQL, a prompt-injection fixture, restart in the middle of a job, and the upgrade of an existing graph schema.

## Known limits

- Tested on one small fictional corpus. Extraction quality, cost and speed on large real documents are not measured.
- Document text is sent to the extraction model. Choose the model with that in mind.
- The name rules are English only. Two different things with the same proper name in one collection merge.
- Validation proves a relation is grounded in the text, not that it is right. One wrong relation was seen with a real but unrelated evidence sentence. A document can also plant a false fact. Agents must treat graph content as data.
- At most 3 hops per call. Longer questions need two calls, which the tool answer suggests.
- No "summarise the whole corpus" answers and no time-aware edges.
- Curated entities have no name embedding, so a question finds them by name or alias only, not by meaning. A fact set holds at most 2000 entities and 5000 relations; there is no per-fact edit, only replacing the whole set.
- A failed job stays failed until the document is ingested again.
- The per-key budget size at the gateway is shared by all keys; only the kb-side `tokensPerHour` is specific to extraction.

Decision record: ADR 0004 (`docs/decisions/0004-knowledge-graph.md`).
