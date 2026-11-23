# Ink data storage

Ink is the catalogue API that serves product names, prices and stock levels. Ink stores its data in cluster Onyx, which has more memory per node than most clusters. The catalogue is rebuilt from the product database every few hours, so a lost node costs only a short delay. Large images are not stored in the database but in object storage behind a cache. Ink exposes a small admin interface that is reachable only from the office network.
