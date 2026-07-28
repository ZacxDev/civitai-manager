// civitai-manager ComfyUI helper — frontend half.
//
// It opens a workflow that civitai-manager saved into this ComfyUI's user
// workflow store, either from a ?cm_open=<path> URL param or from the
// "civitai-manager.open" websocket event (so an already-open tab jumps to it
// without a reload or a duplicate tab).
//
// HARD RULE: this script must never break the editor. Every undocumented
// frontend API is feature-detected before it is called, everything is wrapped
// in try/catch, and any missing piece degrades to a console.debug + a no-op.
// The user can always open the workflow by hand from the Workflows menu.

import { app } from "../../scripts/app.js";
import { api } from "../../scripts/api.js";

const TAG = "[civitai-manager]";
// URL param namespaced to this tool (ComfyUI has no ?workflow= param of its own).
const PARAM = "cm_open";
const EVENT = "civitai-manager.open";
// ComfyWorkflow.basePath in the ComfyUI frontend. Workflow store keys are the
// saved path INCLUDING this prefix.
const BASE = "workflows/";
// Fallback delay (ms) used only if the afterConfigureGraph hook never fires, so
// a frontend that restores no initial graph still honours ?cm_open=.
const FALLBACK_MS = 4000;

function debug() {
  try {
    console.debug.apply(console, [TAG].concat(Array.prototype.slice.call(arguments)));
  } catch (_) {
    /* never throw from logging */
  }
}

// normalizeKey turns an untrusted path into a contained "<dir>/<file>.json" key
// relative to the workflows directory, or null when it is not safe/usable.
function normalizeKey(raw) {
  if (typeof raw !== "string") return null;
  let key = raw.trim();
  if (!key) return null;
  key = key.replace(/^\/+/, "");
  if (key.slice(0, BASE.length) === BASE) key = key.slice(BASE.length);
  if (!key) return null;
  if (key.indexOf("\\") !== -1) return null;
  if (key.split("/").indexOf("..") !== -1) return null;
  if (key.slice(-5).toLowerCase() !== ".json") key += ".json";
  return key;
}

// openWorkflowByKey loads the saved workflow at workflows/<key> into the editor,
// binding the editor tab to the REAL saved file (not an unsaved duplicate).
// Returns true only when the graph was actually loaded.
async function openWorkflowByKey(raw) {
  const key = normalizeKey(raw);
  if (!key) {
    debug("ignoring an unusable workflow path:", raw);
    return false;
  }
  const store = app && app.extensionManager && app.extensionManager.workflow;
  if (!store || typeof store.getWorkflowByPath !== "function") {
    debug("this ComfyUI frontend has no workflow store API — leaving the editor untouched");
    return false;
  }
  const path = BASE + key;
  let wf = null;
  try {
    wf = store.getWorkflowByPath(path);
    if (!wf && typeof store.syncWorkflows === "function") {
      await store.syncWorkflows();
      wf = store.getWorkflowByPath(path);
    }
  } catch (err) {
    debug("workflow lookup failed:", err);
    return false;
  }
  if (!wf) {
    debug("no saved workflow at", path);
    return false;
  }
  try {
    if (typeof wf.load === "function") await wf.load();
    if (typeof store.openWorkflow === "function") await store.openWorkflow(wf);
    if (typeof app.loadGraphData === "function" && wf.activeState) {
      await app.loadGraphData(wf.activeState, true, true, wf);
    } else {
      debug("loadGraphData/activeState unavailable — the workflow tab is open but the graph was not swapped");
      return false;
    }
  } catch (err) {
    debug("could not open", path, err);
    return false;
  }
  debug("opened", path);
  return true;
}

// stripParam removes ?cm_open= from the address bar so a manual reload does not
// re-open the workflow behind the user's back. Best-effort only.
function stripParam() {
  try {
    const url = new URL(window.location.href);
    if (!url.searchParams.has(PARAM)) return;
    url.searchParams.delete(PARAM);
    window.history.replaceState({}, "", url.pathname + (url.search || "") + url.hash);
  } catch (err) {
    debug("could not clean the URL:", err);
  }
}

// `pending` holds a requested workflow until the editor is ready for it.
//
// Nothing may be opened during setup(): ComfyUI restores its OWN previous
// workflow after extension setup() returns, which would clobber our load. That
// applies to a websocket request that arrives early just as much as to the
// ?cm_open= parameter, so BOTH go through this queue.
let pending = null;
let editorReady = false;
let inFlight = false;
let attempts = 0;

// MAX_ATTEMPTS bounds retries so a workflow that simply cannot be opened does not
// re-attempt on every subsequent graph load for the rest of the session.
const MAX_ATTEMPTS = 3;

// drain opens the queued workflow if the editor is ready. `pending` is cleared
// only on SUCCESS, so a failed first attempt is retried by the fallback timer or
// the next afterConfigureGraph rather than being silently dropped.
async function drain(reason) {
  if (!pending || inFlight || !editorReady) return;
  if (attempts >= MAX_ATTEMPTS) {
    debug("giving up on", pending, "after", attempts, "attempts");
    pending = null;
    return;
  }
  attempts++;
  inFlight = true;
  const key = pending;
  try {
    debug("opening the requested workflow (" + reason + ", attempt " + attempts + ")");
    if (await openWorkflowByKey(key)) {
      pending = null;
      attempts = 0;
    }
  } finally {
    inFlight = false;
  }
}

// request queues a workflow and opens it as soon as the editor is ready.
function request(key, reason) {
  if (!key) return;
  pending = key;
  attempts = 0;
  drain(reason).catch(function (err) {
    debug("open failed:", err);
  });
}

try {
  app.registerExtension({
    name: "civitai-manager.open-workflow",

    async setup() {
      // (a) Jump an already-open tab when civitai-manager broadcasts. This goes
      // through the SAME queue as the URL parameter: an event that lands while
      // ComfyUI is still restoring its previous workflow would otherwise be
      // clobbered by that restore.
      try {
        if (api && typeof api.addEventListener === "function") {
          api.addEventListener(EVENT, function (event) {
            const detail = event && event.detail;
            request(detail && detail.path, "websocket");
          });
        } else {
          debug("no api.addEventListener — the jump-an-open-tab path is disabled");
        }
      } catch (err) {
        debug("could not subscribe to the open event:", err);
      }

      // (b) Honour ?cm_open=<path> on a fresh page load. Deferred: see `pending`.
      try {
        const params = new URLSearchParams(window.location.search);
        const requested = params.get(PARAM);
        if (requested) {
          pending = requested;
          attempts = 0;
          stripParam();
        }
      } catch (err) {
        debug("could not read the open parameter:", err);
      }

      // Backstop: a frontend that restores no initial graph never fires
      // afterConfigureGraph, so release the queue after a short delay regardless.
      setTimeout(function () {
        editorReady = true;
        drain("fallback timer").catch(function (err) {
          debug("deferred open failed:", err);
        });
      }, FALLBACK_MS);
    },

    // afterConfigureGraph fires once the editor has finished loading a graph —
    // for the initial load that is the earliest point at which our own load will
    // not be clobbered by ComfyUI's restore.
    async afterConfigureGraph() {
      editorReady = true;
      try {
        await drain("after graph load");
      } catch (err) {
        debug("deferred open failed:", err);
      }
    },
  });
} catch (err) {
  debug("could not register the extension:", err);
}
