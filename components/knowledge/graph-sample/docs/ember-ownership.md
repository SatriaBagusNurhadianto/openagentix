# Ember service ownership

Ember is the notification fan-out service that turns events into emails and push messages. Service Ember is owned by team Granite, which also keeps its runbooks up to date. Changes to the message templates are reviewed by the Granite content owner. The service scales on queue depth rather than on CPU, which keeps the cost steady during campaigns. A weekly report lists the templates that were never used.
