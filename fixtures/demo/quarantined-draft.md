---
title: Quarantined Draft
secret: [not, a, bool]
---

This draft's frontmatter carries a broken type (`secret` is a list, not
a bool), so the parser quarantines the page and fails it closed
(GM-only, never a crash). It is the vault's single deliberate
quarantine — everything else parses with `quarantined=false`.
