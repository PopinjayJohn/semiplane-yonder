---
title: Research Notes
tags: [research]
---

# Open Quests

```dataview
TABLE status, priority
FROM "quests"
WHERE status = "active"
```

Some prose between.

```dataviewjs
dv.table(["Name"], dv.pages('"npc"').map(p => [p.file.name]))
```

![[battle-map.canvas]]

Regular code still renders:

```go
fmt.Println("hello")
```
