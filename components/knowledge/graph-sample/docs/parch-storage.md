# Parch data storage

Parch holds delivery preferences for every customer and is read far more often than it is written. Parch stores its data in cluster Amber, in a database that is replicated to a standby. The standby is promoted by hand because an automatic switch once caused a split brain during a network test. Preference changes are written through a queue so that bursts do not slow down readers. A nightly job removes preferences of deleted accounts.
