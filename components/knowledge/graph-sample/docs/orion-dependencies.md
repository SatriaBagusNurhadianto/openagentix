# Orion downstream calls

During order placement the checkout flow makes a short series of downstream calls. Orion calls the ledger API Quill to record each payment intent before it confirms the order. If Quill does not answer within two seconds the request is retried once and then queued for later reconciliation. The retry budget was chosen after a load test in the staging environment. Nothing in the call path is cached, so every confirmation produces one ledger write.
