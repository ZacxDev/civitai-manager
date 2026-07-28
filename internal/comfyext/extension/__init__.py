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

# _REGISTERED_ATTR is set on the shared PromptServer instance the FIRST time this
# extension registers its routes.
#
# Why this matters: ComfyUI's loader imports every directory under custom_nodes/
# (dot-directories included), so a SECOND copy of this package — a `.bak` folder, a
# ComfyUI-Manager clone, two roots sharing custom_nodes/ via symlink, or a staging
# directory orphaned by a killed install — would run this module again and append
# the SAME routes to the shared route table. ComfyUI then calls add_routes with no
# de-duplication and aiohttp RAISES ("Added route will never be executed, method
# HEAD is already registered"), which aborts startup for the WHOLE server, across
# every custom node. The flag lives on PromptServer, not on a module global,
# because the two copies are distinct modules with distinct globals.
_REGISTERED_ATTR = "_civitai_manager_helper_registered"

# Content types accepted by the open route. See _reject_forged_request.
_JSON_CT = "application/json"

# Sec-Fetch-Site values that mean "not initiated by another site".
_SAME_SITE = ("same-origin", "same-site", "none")


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


def _reject_forged_request(request):
    """Return a rejection reason if this POST looks cross-site, else None.

    ComfyUI is auth-free by design, but this route is NEW surface WE add and it
    has a real consequence: it makes the open editor swap graphs, which can
    discard the user's unsaved edits. Without a check, any page the user visits
    could fire a "simple" cross-origin POST (text/plain, no preflight, no
    response read, so CORS never blocks it) and silently wipe their work.

    Requiring application/json defeats that class outright — a cross-origin JSON
    POST is no longer a simple request, so the browser must preflight, and we
    answer no CORS headers. Sec-Fetch-Site and Origin are checked as well, for
    browsers/proxies that behave differently.
    """
    ctype = (request.headers.get("Content-Type") or "").split(";")[0].strip().lower()
    if ctype != _JSON_CT:
        return "this endpoint only accepts %s requests" % _JSON_CT
    site = (request.headers.get("Sec-Fetch-Site") or "").strip().lower()
    if site and site not in _SAME_SITE:
        return "cross-site requests are not accepted"
    origin = (request.headers.get("Origin") or "").strip()
    if origin:
        # A same-origin browser request carries an Origin matching the Host we
        # were reached on. Non-browser callers (civitai-manager) send none.
        host = (request.headers.get("Host") or "").strip().lower()
        try:
            from urllib.parse import urlsplit

            if host and urlsplit(origin).netloc.lower() != host.lower():
                return "cross-origin requests are not accepted"
        except Exception:
            return "cross-origin requests are not accepted"
    return None


try:
    from server import PromptServer

    _server = PromptServer.instance
    if _server is None:
        raise RuntimeError("PromptServer has no instance yet")
    if getattr(_server, _REGISTERED_ATTR, False):
        # A second copy of this package is present. Registering again would make
        # aiohttp raise later, during ComfyUI's add_routes, which cannot be caught
        # from here and would abort the whole server's startup. Raise NOW, inside
        # this try, so this copy simply does nothing and ComfyUI starts normally.
        raise RuntimeError(
            "another copy of the civitai-manager helper already registered its "
            "routes; this duplicate is inert. Remove the extra custom_nodes "
            "directory so only one copy remains."
        )

    from aiohttp import web

    _routes = _server.routes

    @_routes.get("/civitai-manager/ping")
    async def _civitai_manager_ping(_request):
        """Feature-detection probe. Cheap, side-effect free, no auth needed."""
        return web.json_response(
            {"tool": "civitai-manager", "version": EXTENSION_VERSION}
        )

    @_routes.post("/civitai-manager/open")
    async def _civitai_manager_open(request):
        """Ask any open editor tab to jump to a saved workflow."""
        forged = _reject_forged_request(request)
        if forged is not None:
            return web.json_response({"error": forged}, status=403)
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

    # Only claim the registration once the decorators have actually run.
    setattr(_server, _REGISTERED_ATTR, True)
    _log.info("civitai-manager helper %s: routes registered", EXTENSION_VERSION)
except Exception as exc:  # pragma: no cover - defensive: never break startup
    # A ComfyUI whose server API differs must still start normally. The frontend
    # half (?cm_open=) keeps working without these routes; only the
    # jump-an-open-tab and feature-detection paths are lost.
    _log.warning("civitai-manager helper: HTTP routes not registered (%s)", exc)
