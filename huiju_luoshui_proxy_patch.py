"""Huiju runtime redirect for Luoshui compiled modules."""

import builtins
import datetime
import os
import sys

LOCAL_ORIGIN = "http://localhost:5400"
REMOTE_ORIGINS = (
    "https://api.aibh.site",
    "http://api.aibh.site",
    "https://xiaoluo.site",
    "http://xiaoluo.site",
    "https://43.160.253.170:3000",
    "http://43.160.253.170:3000",
)
REMOTE_UPLOAD_ENDPOINTS = (
    "https://imageproxy.zhongzhuan.chat/api/upload",
    "http://imageproxy.zhongzhuan.chat/api/upload",
)
ENABLED = os.environ.get("LUOSHUI_USE_EXTERNAL_PROXY") == "1"
LOG_PATH = os.path.join(os.path.dirname(os.path.abspath(__file__)), "luoshui_openai_patch.log")
ACTIVE_PATH = os.path.join(os.path.dirname(os.path.abspath(__file__)), "huiju_runtime_patch.active")
MARK = "__huiju_luoshui_proxy_patched__"


def log(message):
    try:
        stamp = datetime.datetime.now().strftime("%Y-%m-%d %H:%M:%S")
        with open(LOG_PATH, "a", encoding="utf-8") as stream:
            stream.write("[%s] %s\n" % (stamp, message))
    except Exception:
        pass


def redirect(value):
    text = str(value)
    for endpoint in REMOTE_UPLOAD_ENDPOINTS:
        if text.startswith(endpoint):
            return LOCAL_ORIGIN + "/upload" + text[len(endpoint):]
    for origin in REMOTE_ORIGINS:
        if text.startswith(origin):
            return LOCAL_ORIGIN + text[len(origin):]
    return text


def patch_httpx(module):
    if getattr(module, MARK, False):
        return
    url_class = getattr(module, "URL", None)
    for class_name in ("Client", "AsyncClient"):
        cls = getattr(module, class_name, None)
        original = getattr(cls, "send", None) if cls else None
        if original is None or getattr(original, MARK, False):
            continue
        if class_name == "AsyncClient":
            async def send(self, request, *args, __original=original, **kwargs):
                old = str(request.url)
                new = redirect(old)
                if new != old:
                    request.url = url_class(new)
                    log("redirect httpx async %r -> %r" % (old, new))
                return await __original(self, request, *args, **kwargs)
        else:
            def send(self, request, *args, __original=original, **kwargs):
                old = str(request.url)
                new = redirect(old)
                if new != old:
                    request.url = url_class(new)
                    log("redirect httpx %r -> %r" % (old, new))
                return __original(self, request, *args, **kwargs)
        setattr(send, MARK, True)
        cls.send = send
    setattr(module, MARK, True)


def patch_requests(module):
    if getattr(module, MARK, False):
        return
    session = getattr(module, "Session", None)
    original = getattr(session, "request", None) if session else None
    if original is None:
        return
    def request(self, method, url, *args, **kwargs):
        new = redirect(url)
        if new != str(url):
            log("redirect requests %r -> %r" % (str(url), new))
        return original(self, method, new, *args, **kwargs)
    setattr(request, MARK, True)
    session.request = request
    setattr(module, MARK, True)


def patch_aiohttp(module):
    if getattr(module, MARK, False):
        return
    session = getattr(module, "ClientSession", None)
    original = getattr(session, "_request", None) if session else None
    if original is None:
        return
    async def request(self, method, url, *args, **kwargs):
        new = redirect(url)
        if new != str(url):
            log("redirect aiohttp %r -> %r" % (str(url), new))
        return await original(self, method, new, *args, **kwargs)
    setattr(request, MARK, True)
    session._request = request
    setattr(module, MARK, True)


def patch_openai(module):
    if getattr(module, MARK, False):
        return
    for class_name in ("OpenAI", "AsyncOpenAI"):
        cls = getattr(module, class_name, None)
        original = getattr(cls, "__init__", None) if cls else None
        if original is None or getattr(original, MARK, False):
            continue
        def init(self, *args, __original=original, **kwargs):
            base_url = kwargs.get("base_url")
            if base_url is None or any(origin in str(base_url) for origin in REMOTE_ORIGINS):
                kwargs["base_url"] = LOCAL_ORIGIN + "/v1"
                log("redirect OpenAI base_url %r -> local bridge" % base_url)
            if not kwargs.get("api_key"):
                kwargs["api_key"] = "local-proxy"
            return __original(self, *args, **kwargs)
        setattr(init, MARK, True)
        cls.__init__ = init
    setattr(module, MARK, True)


ORIGINAL_IMPORT = builtins.__import__


def patched_import(name, globals=None, locals=None, fromlist=(), level=0):
    module = ORIGINAL_IMPORT(name, globals, locals, fromlist, level)
    try:
        root = name.split(".", 1)[0]
        loaded = sys.modules.get(root)
        if root == "httpx" and loaded:
            patch_httpx(loaded)
        elif root == "requests" and loaded:
            patch_requests(loaded)
        elif root == "aiohttp" and loaded:
            patch_aiohttp(loaded)
        elif root == "openai" and loaded:
            patch_openai(loaded)
    except Exception as exc:
        log("patch import %s failed: %r" % (name, exc))
    return module


if ENABLED:
    builtins.__import__ = patched_import
    log("Huiju runtime proxy patch enabled")
    try:
        with open(ACTIVE_PATH, "w", encoding="utf-8") as stream:
            stream.write("pid=%s time=%s\n" % (os.getpid(), datetime.datetime.now().isoformat()))
    except Exception as exc:
        log("write active marker failed: %r" % exc)
    for name, patcher in (("httpx", patch_httpx), ("requests", patch_requests), ("aiohttp", patch_aiohttp), ("openai", patch_openai)):
        try:
            patcher(ORIGINAL_IMPORT(name))
        except Exception as exc:
            log("initial patch %s failed: %r" % (name, exc))
else:
    log("Huiju runtime proxy patch skipped: environment is disabled")
