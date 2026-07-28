"""civitai-manager ComfyUI helper.

Installed (and removable) from civitai-manager's "Open in ComfyUI" panel. It
registers NO nodes and changes nothing about how ComfyUI runs. All it adds is:

  * GET  /civitai-manager/ping  -> {"tool": "civitai-manager", "version": ...}
        feature detection, so civitai-manager knows the helper is present.
  * POST /civitai-manager/open  -> {"path": "civitai-manager/<file>.json"}
        broadcasts a websocket event so an ALREADY-OPEN ComfyUI tab jumps to
        that saved workflow (no page reload, no duplicate tab).
  * web/civitai_manager.js — the frontend half: it opens the named workflow,
        either from the websocket event or from a ?cm_open=<path> URL param.

To remove it, delete this directory and restart ComfyUI (or use the "Remove
helper" button in civitai-manager).

Route registration happens at ComfyUI startup, so installing/removing this
directory requires ONE ComfyUI restart to take effect.
"""

import logging

# EXTENSION_VERSION is reported by /civitai-manager/ping and is written into the
# install marker by civitai-manager, so an upgrade is detectable. Keep it in sync
# with comfyext.ExtensionVersion on the Go side.
EXTENSION_VERSION = "1"

# ComfyUI's custom-node contract. Empty mappings = this package adds no nodes;
# WEB_DIRECTORY makes ComfyUI serve ./web at /extensions/<dir-name>/.
NODE_CLASS_MAPPINGS = {}
NODE_DISPLAY_NAME_MAPPINGS = {}
WEB_DIRECTORY = "./web"

__all__ = ["NODE_CLASS_MAPPINGS", "NODE_DISPLAY_NAME_MAPPINGS", "WEB_DIRECTORY"]

_log = logging.getLogger(__name__)

# Websocket event type the frontend script listens for. ComfyUI re-dispatches any
# unknown message type to listeners registered through the frontend `api` object.
_EVENT = "civitai-manager.open"

# _MAX_PATH bounds the workflow path we will echo into a websocket broadcast.
_MAX_PATH = 512


def _is_contained_relpath(path):
    """Report whether path is a safe, relative, contained workflow path.

    The value is broadcast to every connected editor tab, so it is validated
    here as well as on the sender's side: it must be a non-empty relative path
    with no backslashes, no absolute prefix, and no ".." component.
    """
    if not isinstance(path, str):
        return False
    if not path or len(path) > _MAX_PATH:
        return False
    if path.startswith("/") or "\\" in path or "\x00" in path:
        return False
    return ".." not in path.split("/")


try:
    from aiohttp import web
    from server import PromptServer

    _routes = PromptServer.instance.routes

    @_routes.get("/civitai-manager/ping")
    async def _civitai_manager_ping(_request):
        """Feature-detection probe. Cheap, side-effect free, no auth needed."""
        return web.json_response(
            {"tool": "civitai-manager", "version": EXTENSION_VERSION}
        )

    @_routes.post("/civitai-manager/open")
    async def _civitai_manager_open(request):
        """Ask any open editor tab to jump to a saved workflow."""
        try:
            body = await request.json()
        except Exception:
            return web.json_response({"error": "invalid json body"}, status=400)
        path = body.get("path") if isinstance(body, dict) else None
        if not _is_contained_relpath(path):
            return web.json_response(
                {"error": "path must be a contained relative workflow path"},
                status=400,
            )
        PromptServer.instance.send_sync(_EVENT, {"path": path})
        return web.json_response({"ok": True, "path": path})

    _log.info("civitai-manager helper %s: routes registered", EXTENSION_VERSION)
except Exception as exc:  # pragma: no cover - defensive: never break startup
    # A ComfyUI whose server API differs must still start normally. The frontend
    # half (?cm_open=) keeps working without these routes; only the
    # jump-an-open-tab and feature-detection paths are lost.
    _log.warning("civitai-manager helper: HTTP routes not registered (%s)", exc)
