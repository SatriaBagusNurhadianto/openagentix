## What and why

<!-- What does this change? Why is it needed? Link the issue or ADR. -->

## Tests run

<!-- Paste the commands and the result. Tests come first. Example: `make test`, `charts/openagentix/tests/run.sh`. Say if you also ran it on a cluster and what you saw. -->

```
```

## Checklist

- [ ] Every commit is signed off (`git commit -s`, Developer Certificate of Origin). See CONTRIBUTING.md.
- [ ] A test fails without this change and passes with it.
- [ ] Docs updated (README, `docs/`, or the diary) where behaviour changed.
- [ ] `CHANGELOG.md` updated under `[Unreleased]` for anything a user can see.
- [ ] A new feature is off by default, and the default render is unchanged (chart changes).
- [ ] An ADR is added or updated if this changes a public resource, a dependency, or a security default.
- [ ] No secrets, tokens or personal data in the change, the description, or the logs.
- [ ] A new third-party dependency is listed in `THIRD-PARTY.md` with its licence.

## Security impact

<!-- Does this change who can call what, a NetworkPolicy, RBAC, a default, what is logged, or what leaves the cluster? Write "none" if none, and say why. -->
