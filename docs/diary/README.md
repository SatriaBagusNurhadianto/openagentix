---
title: Maintainer notes
sidebar_position: 99
description: How to keep the OpenAgentix feature diary up to date.
---

# Maintainer notes

This diary is a living record. It says what OpenAgentix does today, how each part was proven, and what is still missing. It is the source of the documentation site, so every claim must come from the repository: the README, the CHANGELOG, the decision records in `docs/decisions/`, the component notes and the code. If a claim cannot be traced to one of these, do not write it.

At each milestone, update the page of the feature you changed (`features/`), the status table in `what-works.md`, and add one entry at the bottom of `timeline.md` with the date and the commit ids. Conventions: plain, short sentences; no marketing words; no emoji; no secrets, tokens, chat ids, e-mail addresses or personal names (use placeholders such as `<your chat id>`); diagrams are fenced `mermaid` blocks; each file starts with `title`, `sidebar_position` and `description`; the files are plain Markdown, not MDX, so keep angle brackets and curly braces inside backticks or code blocks. Use exactly one of these states in the status table: working and proven live, built and tested only, experimental, not built. When sources disagree, prefer the newest CHANGELOG entry or decision record and note the difference.
