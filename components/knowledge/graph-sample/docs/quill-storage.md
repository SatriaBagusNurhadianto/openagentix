# Quill data storage

Quill is the ledger API that records payment intents and settlements. Quill stores its data in cluster Teal, using a dedicated database with synchronous replication. Backups are taken every night and are kept for thirty days. Schema changes to the ledger tables need a written review because rollbacks are slow. Read replicas serve the reporting queries so that the write path stays quiet.
