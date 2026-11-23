# Benchmark results, 2026-11-23

Raw output of `components/bench/run.sh` on the kind demo node (6 CPU, 8 GB). See [README](README.md) for the method and [ADR 0005](../../docs/decisions/0005-runtime-speed-kagent-vs-ax.md) for the reading.

Known defects of this run: the cold-start phase for AX in run 1 followed the concurrency phase on a loaded node; in run 2 the `ax-api-cold` rows failed at once because the throw-away adapter was not ready (a harness fault, not an AX result). In the kagent breakdown table of run 1, the rows `kagent-lean | a | turn 2-4` and two of the seven `turn 1` rows are the concurrency requests.

## Run 1: main phases

### Sequential

| group | backend | n | success | throttled (429, excluded) | median s | p90 s | min s | max s | median tokens | median model calls |
|---|---|---|---|---|---|---|---|---|---|---|
| a | kagent-nomem | 5 | 5/5 | 0 | 2.5 | n/a | 2.2 | 15.0 | 7648 | 1 |
| a | kagent-lean | 5 | 5/5 | 0 | 2.9 | n/a | 2.2 | 13.7 | 3397 | 1 |
| a | ax-claude | 5 | 5/5 | 0 | 7.6 | n/a | 5.2 | 9.7 | 8919 | 1 |
| a | ax-api | 5 | 5/5 | 0 | 3.4 | n/a | 3.0 | 5.2 | 1214 | 1 |
| a | kagent | 5 | 5/5 | 0 | 2.8 | n/a | 2.4 | 14.5 | 8005 | 1 |
| a | ax-api-cold | 2 | 2/2 | 0 | 11.2 | n/a | 5.8 | 16.6 | 1214 | 1 |
| a | ax-api-warm | 2 | 2/2 | 0 | 6.5 | n/a | 5.3 | 7.7 | 1214 | 1 |
| a | ax-claude-cold | 2 | 2/2 | 0 | 52.9 | n/a | 17.1 | 88.6 | 8919 | 1 |
| a | ax-claude-warm | 2 | 2/2 | 0 | 15.5 | n/a | 13.5 | 17.6 | 8919 | 1 |
| b | kagent-nomem | 5 | 5/5 | 0 | 6.5 | n/a | 6.1 | 15.8 | 33006 | 2 |
| b | kagent-lean | 5 | 5/5 | 0 | 6.0 | n/a | 5.4 | 18.9 | 24460 | 2 |
| b | ax-claude | 5 | 5/5 | 0 | 11.1 | n/a | 9.4 | 19.7 | 35682 | 2 |
| b | ax-api | 5 | 5/5 | 0 | 7.4 | n/a | 6.5 | 8.1 | 11225 | 2 |
| b | kagent | 5 | 5/5 | 0 | 6.9 | n/a | 6.1 | 26.5 | 33686 | 2 |
| c | kagent-nomem | 5 | 5/5 | 0 | 3.7 | n/a | 3.7 | 8.3 | 15634 | 2 |
| c | kagent-lean | 5 | 5/5 | 0 | 8.9 | n/a | 3.6 | 9.8 | 7146 | 2 |
| c | ax-claude | 5 | 5/5 | 0 | 7.7 | n/a | 6.1 | 8.8 | 18239 | 3 |
| c | ax-api | 5 | 5/5 | 0 | 5.7 | n/a | 4.5 | 10.8 | 2742 | 2 |
| c | kagent | 5 | 5/5 | 0 | 4.5 | n/a | 3.9 | 10.1 | 16348 | 2 |
| d | kagent-nomem | 5 | 5/5 | 0 | 9.9 | n/a | 8.0 | 22.0 | 23333 | 3 |
| d | kagent-lean | 5 | 5/5 | 0 | 8.4 | n/a | 7.1 | 24.5 | 10349 | 3 |
| d | ax-claude | 5 | 5/5 | 0 | 17.8 | n/a | 13.8 | 19.0 | 27007 | 3 |
| d | ax-api | 5 | 5/5 | 0 | 10.4 | n/a | 8.6 | 12.6 | 3728 | 3 |
| d | kagent | 5 | 5/5 | 0 | 18.0 | n/a | 14.3 | 19.2 | 33057 | 4 |
| e | kagent-nomem | 5 | 5/5 | 0 | 2.9 | n/a | 2.6 | 13.4 | 7672 | 1 |
| e | kagent-lean | 5 | 5/5 | 0 | 3.0 | n/a | 2.5 | 13.2 | 3508 | 1 |
| e | ax-claude | 5 | 5/5 | 0 | 5.5 | n/a | 4.6 | 6.2 | 8977 | 1 |
| e | ax-api | 5 | 5/5 | 0 | 4.0 | n/a | 3.0 | 4.3 | 1240 | 1 |
| e | kagent | 5 | 5/5 | 0 | 3.3 | n/a | 2.9 | 4.4 | 8097 | 1 |

### Concurrency (K identical plain questions at once)

| backend | K | rounds | success | throttled | median request s | max request s | median batch wall s |
|---|---|---|---|---|---|---|---|
| kagent-lean | 4 | 2 | 7/8 | 0 | 56.5 | 63.2 | 62.6 |
| ax-api | 4 | 2 | 8/8 | 0 | 19.0 | 49.8 | 35.8 |

### kagent (Jaeger spans), medians per backend, group and turn

| backend | group | turn | n | median total s | pre-dispatch s | model s | embeddings s | tools s | remainder s | median model calls | median prompt tokens/call | cache-read reported |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| kagent-nomem | a | 1 | 5 | 2.5 | 1.0 | 0.9 | 0.0 | 0.0 | 1.6 | 1 | 7641 | no |
| kagent-lean | a | 1 | 7 | 10.3 | 4.7 | 0.9 | 0.0 | 0.0 | 9.4 | 1 | 3390 | no |
| kagent | a | 1 | 5 | 2.8 | 1.0 | 0.9 | 0.1 | 0.0 | 1.8 | 1 | 7998 | no |
| kagent-nomem | b | 1 | 5 | 6.5 | 1.0 | 3.9 | 0.0 | 1.0 | 1.5 | 2 | 16201 | no |
| kagent-lean | b | 1 | 5 | 6.0 | 0.9 | 3.4 | 0.0 | 1.0 | 1.7 | 2 | 11950 | no |
| kagent | b | 1 | 5 | 6.9 | 1.2 | 3.7 | 0.2 | 1.0 | 1.6 | 2 | 16558 | no |
| kagent-nomem | c | 1 | 5 | 3.7 | 0.9 | 2.1 | 0.0 | 0.0 | 1.6 | 2 | 7748 | no |
| kagent-lean | c | 1 | 5 | 8.9 | 6.0 | 2.1 | 0.0 | 0.0 | 6.9 | 2 | 3497 | no |
| kagent | c | 1 | 5 | 4.4 | 1.1 | 2.1 | 0.1 | 0.0 | 2.0 | 2 | 8105 | no |
| kagent-lean | a | 2 | 2 | 33.7 | 2.8 | 1.3 | 0.0 | 0.0 | 32.4 | 1 | 3390 | no |
| kagent-lean | a | 3 | 2 | 57.4 | 14.6 | 1.0 | 0.0 | 0.0 | 56.4 | 1 | 3390 | no |
| kagent-lean | a | 4 | 2 | 54.7 | 11.2 | 1.6 | 0.0 | 0.0 | 53.1 | 1 | 3390 | no |
| kagent-nomem | d | 1 | 5 | 3.1 | 1.2 | 1.1 | 0.0 | 0.0 | 1.9 | 1 | 7649 | no |
| kagent-nomem | d | 2 | 5 | 2.9 | 1.0 | 1.2 | 0.0 | 0.0 | 1.5 | 1 | 7662 | no |
| kagent-nomem | d | 3 | 5 | 3.6 | 1.4 | 1.4 | 0.0 | 0.0 | 2.1 | 1 | 7682 | no |
| kagent-lean | d | 1 | 5 | 2.7 | 1.0 | 1.0 | 0.0 | 0.0 | 1.6 | 1 | 3398 | no |
| kagent-lean | d | 2 | 5 | 2.8 | 1.3 | 0.9 | 0.0 | 0.0 | 1.9 | 1 | 3411 | no |
| kagent-lean | d | 3 | 5 | 3.2 | 1.4 | 1.2 | 0.0 | 0.0 | 2.0 | 1 | 3431 | no |
| kagent | d | 1 | 5 | 9.5 | 5.3 | 2.6 | 0.2 | 0.1 | 6.3 | 2 | 8095 | no |
| kagent | d | 2 | 5 | 3.9 | 1.5 | 0.9 | 0.1 | 0.0 | 2.7 | 1 | 8200 | no |
| kagent | d | 3 | 5 | 4.0 | 1.4 | 1.4 | 0.1 | 0.0 | 2.3 | 1 | 8220 | no |
| kagent-nomem | e | 1 | 5 | 2.7 | 1.1 | 0.9 | 0.0 | 0.0 | 1.8 | 1 | 7641 | no |
| kagent-nomem | e | 2 | 5 | 2.9 | 1.3 | 0.8 | 0.0 | 0.0 | 2.0 | 1 | 7666 | no |
| kagent-lean | e | 1 | 5 | 3.5 | 1.1 | 0.8 | 0.0 | 0.0 | 2.7 | 1 | 3390 | no |
| kagent-lean | e | 2 | 5 | 3.0 | 1.3 | 1.1 | 0.0 | 0.0 | 1.8 | 1 | 3415 | no |
| kagent | e | 1 | 5 | 2.7 | 1.0 | 0.9 | 0.1 | 0.0 | 1.8 | 1 | 7998 | no |
| kagent | e | 2 | 5 | 3.2 | 1.3 | 1.1 | 0.1 | 0.0 | 2.1 | 1 | 8023 | no |

### AX (egress proxy log joined by actor and time, no spans), medians

| backend | group | turn | n joined | median wall s | request start to resumed s | model s (egress) | tools s (egress) | other egress s | remainder s | median model calls |
|---|---|---|---|---|---|---|---|---|---|---|
| ax-claude | a | 1 | 5 | 7.6 | 0.6 | 0.9 | 0.1 | 0.8 | 6.1 | 1 |
| ax-api | a | 1 | 5 | 3.4 | 0.8 | 0.9 | 0.0 | 0.0 | 1.7 | 1 |
| ax-claude | b | 1 | 5 | 11.1 | 0.9 | 3.9 | 0.6 | 0.8 | 5.7 | 2 |
| ax-api | b | 1 | 5 | 7.4 | 0.6 | 3.8 | 0.6 | 0.0 | 2.3 | 2 |
| ax-claude | c | 1 | 5 | 7.7 | 0.6 | 2.1 | 0.1 | 0.7 | 5.0 | 2 |
| ax-api | c | 1 | 5 | 5.7 | 0.6 | 2.4 | 0.0 | 0.0 | 2.6 | 2 |
| ax-api-cold | a | 1 | 2 | 11.2 | 6.0 | 1.4 | 0.4 | 0.0 | 3.4 | 1 |
| ax-api-warm | a | 1 | 2 | 6.5 | 1.1 | 1.0 | 0.7 | 0.0 | 3.8 | 1 |
| ax-claude-cold | a | 1 | 2 | 52.9 | 1.8 | 1.2 | 2.1 | 2.1 | 47.8 | 1 |
| ax-claude-warm | a | 1 | 2 | 15.5 | 2.0 | 0.9 | 0.3 | 1.3 | 12.3 | 1 |
| ax-claude | d | 1 | 5 | 5.6 | 0.5 | 1.1 | 0.1 | 0.8 | 3.9 | 1 |
| ax-claude | d | 2 | 5 | 6.2 | 0.5 | 0.9 | 0.0 | 0.7 | 4.8 | 1 |
| ax-claude | d | 3 | 5 | 5.8 | 0.6 | 0.9 | 0.0 | 0.7 | 4.0 | 1 |
| ax-api | d | 1 | 5 | 3.4 | 0.5 | 0.9 | 0.0 | 0.0 | 2.2 | 1 |
| ax-api | d | 2 | 5 | 4.0 | 0.4 | 0.9 | 0.0 | 0.0 | 2.6 | 1 |
| ax-api | d | 3 | 5 | 3.2 | 0.6 | 0.9 | 0.0 | 0.0 | 1.7 | 1 |
| ax-claude | e | 1 | 5 | 5.4 | 0.7 | 0.8 | 0.0 | 0.8 | 3.7 | 1 |
| ax-claude | e | 2 | 5 | 5.5 | 0.5 | 0.9 | 0.0 | 0.7 | 4.3 | 1 |
| ax-api | e | 1 | 5 | 3.2 | 0.5 | 0.9 | 0.0 | 0.0 | 1.7 | 1 |
| ax-api | e | 2 | 5 | 4.0 | 0.5 | 0.9 | 0.0 | 0.0 | 2.6 | 1 |

Cannot be split for AX: sandbox start-up, the agent process's own time and the gaps between calls are all in `remainder`; prompt tokens and cache use per call are not visible without content.


## Run 2: cold start on a settled node

### Sequential

| group | backend | n | success | throttled (429, excluded) | median s | p90 s | min s | max s | median tokens | median model calls |
|---|---|---|---|---|---|---|---|---|---|---|
| a | ax-api-cold | 2 | 0/2 | 0 | 0.0 | n/a | 0.0 | 0.0 | n/a | n/a |
| a | ax-api-warm | 2 | 1/2 | 0 | 1.5 | n/a | 0.0 | 3.0 | 1214 | 1 |
| a | ax-claude-cold | 2 | 1/2 | 0 | 72.8 | n/a | 10.5 | 135.1 | 8919 | 1 |
| a | ax-claude-warm | 2 | 2/2 | 0 | 8.0 | n/a | 5.9 | 10.1 | 8919 | 1 |
| e | kagent-lean | 4 | 4/4 | 0 | 2.7 | n/a | 2.4 | 4.9 | 3514 | 1 |

kagent-lean rows (seconds): new conversation, first turn: 5.1, 6.6, 2.1, 14.4. Second turn at once: 2.4, 2.6. Second turn after 120 s idle: 4.9, 2.8.
