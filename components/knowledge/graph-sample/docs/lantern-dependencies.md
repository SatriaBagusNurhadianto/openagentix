# Lantern downstream calls

Lantern enriches each suggestion with the product name and its stock status. Lantern calls the catalogue API Ink for that data and caches the answers for five minutes. A cache miss costs about twenty milliseconds, which is within the latency budget of the search box. During the last sale the cache hit rate stayed above ninety percent. The team watches that ratio on a single graph.
