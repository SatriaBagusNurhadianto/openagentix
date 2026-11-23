# Knowledge graph canary corpus

Twenty-six short, fictional documents for checking that the knowledge graph links facts that no single document
states together. Nothing here is real: every service, team, cluster, region and person is invented.

The five **chain documents** each hold exactly one link, stated in ordinary prose:

| Document | Link |
|---|---|
| `docs/orion-ownership.md` | Service Orion is owned by team Basalt |
| `docs/orion-dependencies.md` | Orion calls the ledger API Quill |
| `docs/quill-storage.md` | Quill stores its data in cluster Teal |
| `docs/basalt-on-call.md` | Team Basalt's on-call engineer is Mira Okafor |
| `docs/teal-region-incident.md` | Cluster Teal runs in region Vane-3 and had a disk outage on 3 March |

The other twenty-one documents are distractors in the same vocabulary: other services, teams, clusters, regions and
on-call people, other outages (one more on 3 March, in another region), another Mira (Mira Lindqvist, team Granite), a
person named Mira Okafor who is mentioned without any team, and a second chain
(Sparrow, Quire, cluster Jade in region Vane-3) that looks the same but has no outage.
No document contains more than one link of the chain.

## The canary question

> A cluster in region Vane-3 had a disk outage. Which service calls the API that stores its data in that cluster, and who is the on-call engineer of the team that owns that service?

Expected answer: **Mira Okafor**.

Expected path, one hop per document:

```
Vane-3 <- Teal <- Quill <- Orion <- Basalt -> Mira Okafor
 (teal-region-incident) (quill-storage) (orion-dependencies) (orion-ownership) (basalt-on-call)
```

Text search finds the `teal-region-incident` document for "Vane-3 outage" but nothing in it names the service, the
team or the person. The distractors share most of the question's words ("on call", "team", "owns", "outage"), so the
vector baseline tends to return other teams' rota pages, such as Mira Lindqvist's.

The graph query reaches `Orion` from `Vane-3` in three hops (the traversal limit). The last two hops, `Orion` to
`Mira Okafor`, are a second query, which is how an agent continues a chain with `kb_graph_entity` or `kb_graph_paths`.
`hack/kb-graph-canary.sh` does exactly that and prints PASS when both parts are found with citations to the five
documents.

Ingest it with `hack/kb-ingest.sh components/knowledge/graph-sample/docs graph-canary` (the script does this for you).
The README is outside `docs/` on purpose: it states the answer and must not be ingested.
