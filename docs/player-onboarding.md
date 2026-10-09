# Player Onboarding

How to play at a Yonder table: log in, read, roll, and run your character.
If a term here is unfamiliar, your GM is the authority — this page covers
the tool, not the rules.

## 1. Getting in

Your GM gives you one of:

- **A claim (invitation) link** — open it, pick your username, set a
  password (minimum 8 characters), and create your character with zero
  frontmatter: the wizard stamps ownership for you. Your character sheet
  then lives at `characters/<you>/index.md`.
- **A provisioned account** — a username + password from your GM.

> Status note: the shipped binary identifies demo sessions with `?as=<name>`
> in the address bar (absent = guest), and offers a real login page
> (`/login` with a session cookie) — log in whenever you can. Until the
> demo identity is retired, keep your `?as=<name>` link private the way you
> would a password — anyone holding it reads as you. (Recorded follow-up:
> the demo identity must be retired/gated now that HTTP login is the norm.)

## 2. Reading the wiki

- Anyone (even guests) can read non-secret pages: follow links, use
  `/search`, browse the `/graph`.
- Secret pages show a uniform "not found" to anyone who may not see them —
  that is deliberate. If a link renders redacted (no alias), you cannot see
  its target; ask your GM in person, not by probing URLs.
- `> [!secret]` blocks inside a page you *can* read may still hide text
  from you unless you own the page. Hidden blocks are announced as text by
  screen readers ("secret − hidden"), never as color alone.
- Pages update live: when the GM reveals something, it appears without a
  reload (polite announcements for dice, assertive for reveals). If your
  connection drops, SSE reconnects; a manual reload always works.

## 3. Your sheet (`/me`)

`/me` opens your character sheet (Simple view by default; Advanced shows
the source editor for owner/GM eyes only):

- **HP and hit dice** are tap-friendly buttons (44 px targets) that post
  surgical updates — clamped server-side, so the worst a double-tap does
  is nothing. Hit dice and spell slots render as text pips, never color-only.
- **Rest and level-up** are explicit buttons (short/long rest, level).
  Changes write through to the vault file immediately and reindex.
- Derived numbers come from the active ruleset data; the log pins the full
  modifier list per roll, so a mid-session rule change never rewrites
  history — it applies to future rolls only.
- Your sheet is secret to you (+ your GM and anyone you grant
  `editable-by`). Other players get the same 404 as guests.

## 4. Dice + encounters

- The dice tray is a generic roller (up to 100 dice / 1000 faces). Blind
  GM rolls route only to eyes allowed to see them; the log replays stored
  values with per-viewer re-checks, so a shared link never leaks a blind
  roll.
- During encounters, initiative order and your token HP show on the run
  view. Say your move; the GM (or your token controls, where granted)
  moves the token.

## 5. The table (`/vtt/<map>`)

- The board image, grid, tokens, and fog come from your GM's map setup.
  Fog hides — if an area is dark, your character cannot see it, and neither
  can you (fog fails closed: glitches hide, never reveal).
- Tokens are keyboard-movable (arrow keys, coordinates announced as text)
  as well as draggable with 44 px touch targets. Reduced-motion settings
  are respected.
- Only maps your GM shared with you appear; anything else is a 404.

## 6. Good citizenship

- Vault files are Obsidian-compatible markdown — if your GM lets you edit,
  write plain markdown; never hand-edit another player's `owner:` line
  (ownership transfers are a GM CLI op).
- Uploads (where enabled): PNG/JPG/PDF up to 5 MB images / 10 MB PDFs;
  WebP is stored as-is. SVG is blocked, EXIF location data is stripped.
  Phone photos that arrive rotated need re-saving before upload (EXIF
  orientation is stripped, not applied).
- Print a handout with your browser's print function — the print
  stylesheet renders only what you are allowed to see.
