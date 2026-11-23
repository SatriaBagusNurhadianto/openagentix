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

## Curated facts: `org-catalogue.yaml`

A small hand-written catalogue (19 entities, 12 relations) that goes with the corpus: the manager each team escalates to, the
service tier of five services, and a team (Cobalt, with its service Halyard) that no document mentions. It is loaded with no model
call:

```sh
hack/kb-graph-load.sh graph-canary org-catalogue components/knowledge/graph-sample/org-catalogue.yaml
```

It states none of the five chain links above (not who owns Orion, what Orion calls, where Quill stores data, who is on call for
Basalt, or where Teal runs), so it cannot answer the canary question and does not change its answer. It adds facts the corpus lacks.

### The question that needs both

> Which service calls the ledger API Quill, which team owns it, and who is that team's escalation manager?

Expected answer: **Orion**, owned by **Basalt**, whose escalation manager is **Priya Venkat**.

```
Quill <-calls- Orion <-owns- Basalt -escalates_to-> Priya Venkat
 (extracted: orion-dependencies) (extracted: orion-ownership) (curated: factset org-catalogue)
```

Two links come from documents and are extracted by the model; the last one exists only in the catalogue. In the tool answer it
is marked `curated: factset=org-catalogue source=https://example.com/openagentix/org-catalogue` with its note, and it has no
document id. Without the catalogue the same question reaches Basalt and stops. The look-alike chain stays apart: for the other
ledger API (Quire, called by Sparrow, owned by Granite) the manager is Hana Mori. A second question needs only the catalogue:
"What tier is the service Orion?" answers Tier 1.

An offline test covers this pair (`services/kb/internal/kb/factset_sample_test.go`, on the stored extraction of the canary run). It
has not been run against the cluster or a live model.
