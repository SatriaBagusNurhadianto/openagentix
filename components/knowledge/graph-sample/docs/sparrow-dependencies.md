# Sparrow downstream calls

To build a statement Sparrow needs the posted transactions of each customer. Sparrow calls the ledger API Quire once per customer and walks the result page by page. The calls are spread over several hours so that the ledger is not overloaded at the start of the month. A failed customer is retried at the end of the run instead of blocking the others. The run report lists every customer that needed a second attempt.
