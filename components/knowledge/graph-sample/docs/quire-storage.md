# Quire data storage

Quire is a ledger API that keeps the posted transactions used for statements. Quire stores its data in cluster Jade, next to the internal tools, because its volume is modest. The database is small enough to be restored from a nightly backup in under ten minutes. Because Jade is lightly used, Quire's maintenance is done during business hours. A proposal exists to move Quire to a larger cluster if its volume grows.
