# Cluster Teal region and incident

Cluster Teal runs in region Vane-3 and had a disk outage on 3 March. A failing storage controller made several volumes read-only for about forty minutes. The incident review found that the disk health alerts had been muted during a maintenance window and never re-enabled. Since then the alerts are tied to the maintenance ticket and return automatically when it closes. No data was lost, although some background jobs had to be restarted.
