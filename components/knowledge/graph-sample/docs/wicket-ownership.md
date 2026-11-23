# Wicket service ownership

Wicket is the scheduler that runs nightly batch jobs for finance. Service Wicket is owned by team Tern, which sits inside the finance engineering group. Jobs are defined in a repository that Tern reviews, and failed jobs raise a ticket automatically. The nightly window starts at one in the morning and normally ends before four. Wicket keeps a ninety day history of job runs for auditors.
