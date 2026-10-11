---
title: Edge Cases
---

A malformed body renders the labeled fallback box:

```statblock
name: Broken
stats: [unclosed
```

An empty body is a valid empty box:

```statblock
```

A statblock inside a secret callout keeps its secret flag:

> [!secret]-
> ```statblock
> name: Hidden Horror
> ac: 12
> actions:
>   - name: Whisper
>     desc: It knows [[cinder-pact|the pact]].
> ```

Layout echoes as data-layout, monster is a note only, short stats drop the table:

```statblock
name: Ooze
layout: Wide Frame
monster: Black Pudding
stats: [10, 10]
ac: 7
damage_resistances: acid, cold
condition_immunities: blinded, charmed
reactions:
  - name: Split
    desc: When subjected to lightning or slashing damage, it splits.
bonus_actions:
  - Pseudopod feint
legendary_actions:
  - name: Engulf
    desc: It engulfs a creature in its space.
unknown_key: ignored quietly
```
