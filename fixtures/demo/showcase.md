---
title: Statblocks and Callouts
---

A goblin scout, rendered as a statblock:

```statblock
name: Goblin Scout
size: Small
type: humanoid (goblinoid)
alignment: neutral evil
ac: 15
hp: 7
hit_dice: 2d6
speed: 30 ft.
stats: [8, 14, 10, 10, 8, 8]
saves:
  - dexterity: 4
skillsaves:
  - stealth: 6
senses: darkvision 60 ft., passive Perception 9
languages: Common, Goblin
cr: 1/4
traits:
  - name: Nimble Escape
    desc: The goblin can take the Disengage or Hide action as a bonus action on each of its turns. It learned the trails near [[lore|the cinder plains]].
actions:
  - name: Scimitar
    desc: "Melee Weapon Attack: +4 to hit, reach 5 ft., one target. Hit: 5 (1d6 + 2) slashing damage."
  - name: Shortbow
    desc: "Ranged Weapon Attack: +4 to hit, range 80/320 ft., one target. Hit: 5 (1d6 + 2) piercing damage."
```

An admonition code block:

```ad-tip
title: GM tip
collapse: open

Bearers of the pact walk openly in Ashfall. Offer the vanguard a parley before rolling initiative.
```

A built-in callout with a fold:

> [!warning]+ Watch the cinder gate
> The gate only opens for bearers. Everything else waits outside.
