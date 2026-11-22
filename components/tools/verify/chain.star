# No json module in the sandbox: results are {"text": "<raw JSON string>"}; parse with string ops.
q = osv_query_vulnerability(package_name="lodash", ecosystem="npm", version="4.17.20")["text"]
first_id = q.split('"id": "')[1].split('"')[0]
d = osv_get_vulnerability(id=first_id)["text"]
summary = d.split('"summary": "')[1].split('"')[0]
return {"first_id": first_id, "summary": summary, "n_ids": q.count('"id": "GHSA')}
