// PixiJS parity loader for the VTT board (amend A-VTT2, spike §§2-4).
//
// First-party, served from /static/vtt/ (never a render-blocking script):
// the page's inline module script dynamically imports the vendored
// pixi.js@8.22.0 ESM build (/static/vendor/pixi.js, pinned + SRI-checked via
// `yonder vendor verify`) and then this module, and only when the request
// carries ?renderer=pixi. DOM users fetch zero vendored bytes.
//
// Contract discipline (frozen, unchanged by this file):
//   - State comes ONLY from GET /api/vtt/{mapID}/state (per-viewer filtered
//     server-side; the canvas never receives unfiltered data to clip itself).
//   - Live updates ride a private EventSource over /events on the same beat
//     as clientJS(); every event refetches the snapshot and re-renders. The
//     canvas keeps NO authoritative state.
//   - The DOM board is the operable/a11y truth: on a successful mount the
//     caller sets aria-hidden="true" on it (hidden, never removed). Any
//     failure (no WebGL/Canvas, import failure, asset load failure, scene
//     build throw) leaves the shipped DOM board exactly as SSR'd.
//   - prefers-reduced-motion: no Ticker loop, no tweens — render on state
//     change only (same as the DOM morph beat).
// pixi-board.js is a single-mount loader per page, so the vendored namespace
// is bound once here (bootPixi's parameter) and the scene helpers below read
// it from module scope.
let PIXI_LIB = null;

export async function bootPixi(PIXI, board, opts) {
  PIXI_LIB = PIXI;
  const o = opts || {};
  const reduceMotion = !!o.reduceMotion;
  const cols = o.cols, rows = o.rows;
  if (!PIXI || !board || !cols || !rows || !o.stateURL) return false;

  // The background sprite rides the SAME ACL-checked asset URL the DOM <img>
  // uses (incl. the ?as= identity query). Missing img = no background art:
  // the canvas still renders grid/fog/tokens (fail-closed, never blank).
  const bgImg = board.querySelector("[data-board-image] img");
  const bgURL = bgImg ? bgImg.getAttribute("src") : "";

  let app = null;
  try {
    app = new PIXI.Application();
    await app.init({
      preference: "webgl", resizeTo: board, autoDensity: true,
      resolution: (typeof devicePixelRatio !== "undefined" && devicePixelRatio) || 1,
      backgroundAlpha: 0, antialias: true,
    });
  } catch (e) { return false; }
  if (!app || !app.renderer) return false;
  if (app.renderer.type !== (PIXI.RendererType && PIXI.RendererType.WEBGL)) {
    // v8 canvas fallback: headless/blocked-WebGL basements mount here.
    try {
      await app.init({ preference: "canvas", resizeTo: board, autoDensity: true, backgroundAlpha: 0 });
    } catch (e) { try { app.destroy(); } catch (_) {} return false; }
  }
  if (!app || !app.renderer) return false;
  if (reduceMotion && app.ticker && app.ticker.stop) app.ticker.stop();

  // Scene build: any throw aborts the mount cleanly (caller keeps the DOM).
  let stage, view;
  try {
    stage = new PIXI.Container();
    app.stage.addChild(stage);
    view = { app: app, stage: stage, cols: cols, rows: rows };
    if (bgURL) {
      const tex = await PIXI.Assets.load(bgURL); // throw -> DOM kept
      const sprite = new PIXI.Sprite(tex);
      sprite.eventMode = "none";
      stage.addChild(sprite);
      view.bg = sprite;
    }
    const grid = new PIXI.Graphics();
    drawGrid(grid, app.screen.width, app.screen.height, cols, rows);
    stage.addChild(grid);
    view.grid = grid;
    const fog = new PIXI.Graphics();
    stage.addChild(fog);
    view.fog = fog;
    const walls = new PIXI.Graphics();
    stage.addChild(walls);
    view.walls = walls;
    const doors = new PIXI.Graphics();
    stage.addChild(doors);
    view.doors = doors;
    const lights = new PIXI.Graphics();
    stage.addChild(lights);
    view.lights = lights;
    const tokens = new PIXI.Container();
    stage.addChild(tokens);
    view.tokens = tokens;
    view.render = function (snap) { renderSnapshot(view, snap); };
    if (o.snapshot) renderSnapshot(view, o.snapshot);
  } catch (e) {
    const cv = app.canvas || app.view; // BEFORE destroy: canvas is a getter over renderer.canvas
    try { app.destroy(true); } catch (_) {}
    if (cv && cv.parentNode) cv.parentNode.removeChild(cv);
    return false;
  }

  // Mount AFTER the scene builds (a blank canvas must never replace the DOM).
  board.appendChild(app.canvas);
  app.canvas.setAttribute("data-pixi-canvas", "");
  app.canvas.setAttribute("aria-hidden", "true");
  // The DOM client's SSE morph rewrites #vtt-map's innerHTML on every board
  // event, which would drop this canvas with it: publish the live view at
  // page scope so applyMorph can re-attach the SAME canvas node (the WebGL
  // context survives) and re-hide the DOM board.
  view.canvasEl = app.canvas;
  if (typeof window !== "undefined") window.__pixiView = view;

  // Private SSE subscription: same event names as clientJS(); every board
  // event refetches the (already per-viewer filtered) snapshot and repaints.
  if (o.streamURL) {
    try {
      const src = new EventSource(o.streamURL);
      const refresh = function () {
        fetch(o.stateURL, { headers: { "Accept": "application/json" } })
          .then(function (r) { if (!r.ok) throw new Error("state " + r.status); return r.json(); })
          .then(function (snap) { renderSnapshot(view, snap); })
          .catch(function () { /* next event or reload repaints */ });
      };
      ["token-move", "token-hp", "fog-update", "initiative-update",
       "encounter-spawn", "session-recap"].forEach(function (name) {
        src.addEventListener(name, refresh);
      });
      view.stop = function () { try { src.close(); } catch (e) {} };
    } catch (e) { /* SSE unavailable: initial paint + refresh-on-reload still work */ }
  }
  view.destroy = function () {
    if (view.stop) view.stop();
    if (typeof window !== "undefined" && window.__pixiView === view) window.__pixiView = null;
    try { app.destroy(true); } catch (e) {}
  };
  return view;
}

function screenSize(view) { return { w: view.app.screen.width, h: view.app.screen.height }; }

function px(view, gx, gy) {
  const s = screenSize(view);
  return { x: gx / view.cols * s.w, y: gy / view.rows * s.h };
}

// Grid overlay: 1px lines every cell (cosmetic only; no hit-testing), alpha
// matched to the DOM overlay (opacity .3).
function drawGrid(g, w, h, cols, rows) {
  g.clear();
  g.rect(0, 0, w, h).stroke({ width: 1, color: 0xffffff, alpha: 0.3 });
  for (let i = 1; i < cols; i++) {
    const x = i / cols * w;
    g.moveTo(x, 0).lineTo(x, h).stroke({ width: 1, color: 0xffffff, alpha: 0.3 });
  }
  for (let j = 1; j < rows; j++) {
    const y = j / rows * h;
    g.moveTo(0, y).lineTo(w, y).stroke({ width: 1, color: 0xffffff, alpha: 0.3 });
  }
}

// Fog: v1 rect codec ("v1;<cols>x<rows>;x,y,w,h;..."), the same opaque Shape
// the snapshot carries. Missing/hidden/corrupt -> full cover (fail-closed,
// mirrors ResolveFog). A revealed board carries ZERO rects: nothing is drawn
// (an empty rect list means no hidden areas, not an unreadable mask — filling
// the board there would black out a revealed map).
function drawFog(view, fog) {
  const g = view.fog;
  g.clear();
  const s = screenSize(view);
  if (!fog || fog.hidden) {
    g.rect(0, 0, s.w, s.h).fill({ color: 0x000000, alpha: 0.85 });
    return;
  }
  const shape = String(fog.shape || "");
  const parts = shape.split(";");
  if (parts.length < 2 || parts[0] !== "v1") {
    g.rect(0, 0, s.w, s.h).fill({ color: 0x000000, alpha: 0.85 });
    return;
  }
  for (let i = 2; i < parts.length; i++) {
    const n = parts[i].split(",");
    if (n.length !== 4) continue;
    const x = +n[0], y = +n[1], w = +n[2], h = +n[3];
    if (!isFinite(x) || !isFinite(y) || w <= 0 || h <= 0) continue;
    const a = px(view, x, y), b = px(view, x + w, y + h);
    g.rect(a.x, a.y, b.x - a.x, b.y - a.y).fill({ color: 0x000000, alpha: 0.72 });
  }
}

// R6 full-blocker posture (amend A-VTT2b, v3 snapshot flag): when the
// sidecar marks blocking-objects as sight blockers, objects render at WALL
// weight (full blockers), not the faint movement-only style. Declarative
// until P3 vision feeds them into the LOS mask.
function drawWalls(view, walls, objects, objectsBlockSight) {
  const g = view.walls;
  g.clear();
  const seg = function (s, width, alpha) {
    const a = px(view, s.x1, s.y1), b = px(view, s.x2, s.y2);
    g.moveTo(a.x, a.y).lineTo(b.x, b.y).stroke({ width: width, color: 0x111111, alpha: alpha });
  };
  (walls || []).forEach(function (s) { seg(s, 2, 0.9); });
  const objAlpha = objectsBlockSight ? 0.9 : 0.45;
  (objects || []).forEach(function (s) { seg(s, 2, objAlpha); });
}

// Doors: closed portals read as a solid bar; open (window) portals as a faint
// dotted span. The GM door panel (DOM, text) carries the operable state.
function drawDoors(view, doors) {
  const g = view.doors;
  g.clear();
  (doors || []).forEach(function (d) {
    const a = px(view, d.a[0], d.a[1]), b = px(view, d.b[0], d.b[1]);
    if (d.closed) {
      g.moveTo(a.x, a.y).lineTo(b.x, b.y).stroke({ width: 4, color: 0x8a5a2b, alpha: 0.95 });
    } else {
      g.moveTo(a.x, a.y).lineTo(b.x, b.y).stroke({ width: 1, color: 0x8a5a2b, alpha: 0.35 });
    }
  });
}

// Lights: static soft glow (no shadow casting — vision/los is P3). Range is
// in grid squares.
function drawLights(view, lights) {
  const g = view.lights;
  g.clear();
  const s = screenSize(view);
  (lights || []).forEach(function (l) {
    const c = px(view, l.x, l.y);
    const r = Math.max(4, (l.range || 1) / view.cols * s.w);
    const col = parseInt(String(l.color || "ffffff").replace("#", ""), 16);
    const rgb = isFinite(col) ? (col & 0xffffff) : 0xffffff;
    for (let i = 4; i >= 1; i--) {
      g.circle(c.x, c.y, r * i / 4).fill({ color: rgb, alpha: 0.06 * (5 - i) });
    }
  });
}

// Tokens: one circle per snapshot token, initial via Text, same x/y math as
// the DOM render (left=(x+0.5)*100/cols). Read-only markers in P1 — the DOM
// buttons underneath stay the operable path.
function drawTokens(view, tokens) {
  const layer = view.tokens;
  layer.removeChildren();
  const s = screenSize(view);
  const rad = Math.max(8, Math.min(s.w / view.cols, s.h / view.rows) * 0.4);
  (tokens || []).forEach(function (t) {
    const c = px(view, t.x + 0.5, t.y + 0.5);
    const g = new PIXI_LIB.Graphics();
    g.circle(0, 0, rad).fill({ color: 0xf5f5f5, alpha: 0.95 }).stroke({ width: 2, color: 0x222222 });
    g.label = "";
    g.x = c.x; g.y = c.y;
    const name = String(t.name || t.id || "?");
    const txt = new PIXI_LIB.Text({ text: name.slice(0, 1).toUpperCase(), style: { fontFamily: "system-ui, sans-serif", fontSize: Math.round(rad), fill: 0x111111 } });
    txt.anchor.set(0.5);
    g.addChild(txt);
    g.eventMode = "none";
    layer.addChild(g);
  });
}

function renderSnapshot(view, snap) {
  if (!snap) return;
  const s = screenSize(view);
  if (view.bg) {
    view.bg.width = s.w;
    view.bg.height = s.h;
  }
  drawGrid(view.grid, s.w, s.h, view.cols, view.rows);
  drawFog(view, snap.fog);
  drawWalls(view, snap.walls, snap.objects, snap.objects_block_sight);
  drawDoors(view, snap.doors);
  drawLights(view, snap.lights);
  drawTokens(view, snap.tokens);
}
