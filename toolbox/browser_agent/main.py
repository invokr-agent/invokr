#!/usr/bin/env python3

from __future__ import annotations

import json
import math
import os
import subprocess
import sys
import urllib.parse
from pathlib import Path
from typing import Any

PLUGIN_DIR = Path(__file__).resolve().parent
PLAYWRIGHT_MARKER = Path(sys.prefix) / ".playwright_chromium_installed"

DEFAULT_STATE_ROOT = Path.home() / ".local" / "share" / "invokr" / "browser-agent"
_state_root_override = os.environ.get("INVOKR_TOOL_STATE_DIR", "").strip()
if not _state_root_override:
    _state_root_override = os.environ.get("INVOKR_BROWSER_AGENT_STATE", "").strip()
STATE_ROOT = Path(_state_root_override).expanduser() if _state_root_override else DEFAULT_STATE_ROOT
PROFILE_DIR = STATE_ROOT / "profile"
STATE_PATH = STATE_ROOT / "state.json"

DEFAULT_TIMEOUT_MS = 10000
MAX_READ_CHARS = 10000
MAX_SNAPSHOT_ELEMENTS = 200


def write_result(result: Any = None, error: str | None = None, *, end: str = "") -> None:
    payload: dict[str, Any] = {}
    if result is not None:
        payload["result"] = result if isinstance(result, str) else json.dumps(result)
    if error:
        payload["error"] = error
    sys.stdout.write(json.dumps(payload) + end)


def read_call() -> dict[str, Any]:
    raw = sys.stdin.read()
    if not raw.strip():
        return {"name": "", "arguments": {}}
    try:
        payload = json.loads(raw)
    except json.JSONDecodeError as exc:
        raise ValueError("Invalid JSON input") from exc
    if not isinstance(payload, dict):
        raise ValueError("Tool call must be a JSON object")
    return payload


def get_arguments(call: dict[str, Any]) -> dict[str, Any]:
    args = call.get("arguments")
    if args is None:
        return {}
    if not isinstance(args, dict):
        raise ValueError("arguments must be an object")
    return args


def get_optional_str(arguments: dict[str, Any], key: str) -> str:
    value = arguments.get(key)
    if value is None:
        return ""
    if not isinstance(value, str):
        raise ValueError(f"{key} must be a string")
    return value.strip()


def get_bool(arguments: dict[str, Any], key: str, default: bool = False) -> bool:
    if key not in arguments:
        return default
    value = arguments[key]
    if not isinstance(value, bool):
        raise ValueError(f"{key} must be a boolean")
    return value


def get_timeout_ms(arguments: dict[str, Any]) -> int:
    if "timeout_ms" not in arguments:
        return DEFAULT_TIMEOUT_MS
    value = arguments["timeout_ms"]
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise ValueError("timeout_ms must be a number")
    if not math.isfinite(value):
        raise ValueError("timeout_ms must be finite")
    timeout_ms = int(value)
    if timeout_ms <= 0:
        raise ValueError("timeout_ms must be greater than zero")
    return timeout_ms


def canonical_action(raw_action: str) -> str:
    normalized = raw_action.strip().lower().replace("-", "_").replace(" ", "_")
    aliases = {
        "": "open",
        "open": "open",
        "open_page": "open",
        "navigate": "open",
        "goto": "open",
        "go_to": "open",
        "click": "click",
        "click_element": "click",
        "tap": "click",
        "fill": "fill",
        "fill_input": "fill",
        "type": "fill",
        "enter_text": "fill",
        "press": "press",
        "press_key": "press",
        "key_press": "press",
        "read": "read",
        "read_page": "read",
        "get_text": "read",
        "snapshot": "snapshot",
        "snapshot_page": "snapshot",
        "inspect": "snapshot",
        "wait": "wait",
        "wait_for": "wait",
        "wait_for_element": "wait",
        "close": "close",
        "close_browser": "close",
        "close_context": "close",
    }
    return aliases.get(normalized, normalized)


def normalize_url(url: str) -> str:
    parsed = urllib.parse.urlparse(url)
    if parsed.scheme:
        return url
    return "https://" + url


def build_target(url: str, search: str, default_blank: bool) -> str:
    if url and search:
        raise ValueError("provide either url or search, not both")
    if search:
        return "https://duckduckgo.com/?q=" + urllib.parse.quote_plus(search)
    if url:
        return normalize_url(url)
    if default_blank:
        return "about:blank"
    return ""


def ensure_state_dir() -> None:
    STATE_ROOT.mkdir(parents=True, exist_ok=True)
    PROFILE_DIR.mkdir(parents=True, exist_ok=True)


def load_state() -> dict[str, Any]:
    if not STATE_PATH.exists():
        return {}
    try:
        payload = json.loads(STATE_PATH.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return {}
    if not isinstance(payload, dict):
        return {}
    return payload


def save_state(state: dict[str, Any]) -> None:
    ensure_state_dir()
    STATE_PATH.write_text(json.dumps(state), encoding="utf-8")


def run_checked(command: list[str], failure_prefix: str) -> None:
    proc = subprocess.run(
        command,
        check=False,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
    )
    if proc.returncode == 0:
        return
    detail = (proc.stderr or proc.stdout or "").strip()
    if len(detail) > 400:
        detail = detail[-400:]
    if not detail:
        detail = f"exit code {proc.returncode}"
    raise RuntimeError(f"{failure_prefix}: {detail}")


def ensure_playwright_browser() -> None:
    if not PLAYWRIGHT_MARKER.exists():
        run_checked([sys.executable, "-m", "playwright", "install", "chromium"], "failed to install Playwright Chromium")
        PLAYWRIGHT_MARKER.write_text("ok\n", encoding="utf-8")


def is_blank_url(url: str) -> bool:
    return not url or url == "about:blank"


def maybe_navigate(page: Any, arguments: dict[str, Any], state: dict[str, Any], timeout_ms: int) -> None:
    url = get_optional_str(arguments, "url")
    search = get_optional_str(arguments, "search")
    target = build_target(url, search, default_blank=False)
    if target:
        page.goto(target, wait_until="domcontentloaded", timeout=timeout_ms)
        state["last_url"] = page.url
        return

    last_url = state.get("last_url")
    if is_blank_url(page.url) and isinstance(last_url, str) and last_url.strip():
        page.goto(last_url.strip(), wait_until="domcontentloaded", timeout=timeout_ms)
        state["last_url"] = page.url


def pick_locator(page: Any, arguments: dict[str, Any]) -> Any:
    role = get_optional_str(arguments, "role")
    name = get_optional_str(arguments, "name")
    label = get_optional_str(arguments, "label")
    placeholder = get_optional_str(arguments, "placeholder")
    text = get_optional_str(arguments, "text")
    selector = get_optional_str(arguments, "selector")

    if role:
        kwargs: dict[str, Any] = {"name": name} if name else {}
        return page.get_by_role(role, **kwargs).first
    if label:
        return page.get_by_label(label).first
    if placeholder:
        return page.get_by_placeholder(placeholder).first
    if text:
        return page.get_by_text(text).first
    if selector:
        return page.locator(selector).first

    raise ValueError("No target locator provided. Use role/name, label, placeholder, text, or selector.")


def truncated_text(text: str) -> str:
    if len(text) <= MAX_READ_CHARS:
        return text
    return text[:MAX_READ_CHARS] + "\n...[truncated]"


def snapshot_elements(page: Any) -> list[dict[str, str]]:
    script = """
    (maxElements) => {
      const selectors = [
        "a",
        "button",
        "input",
        "select",
        "textarea",
        "summary",
        "[role]",
        "h1","h2","h3","h4","h5","h6"
      ];

      const isVisible = (el) => {
        const style = window.getComputedStyle(el);
        if (style.display === "none" || style.visibility === "hidden") {
          return false;
        }
        const rect = el.getBoundingClientRect();
        return rect.width > 0 && rect.height > 0;
      };

      const semanticRole = (el) => {
        const tag = el.tagName.toLowerCase();
        if (tag === "a" && el.getAttribute("href")) return "link";
        if (tag === "button") return "button";
        if (tag === "textarea") return "textbox";
        if (tag === "select") return "combobox";
        if (tag === "summary") return "button";
        if (tag.match(/^h[1-6]$/)) return "heading";
        if (tag === "input") {
          const t = (el.getAttribute("type") || "text").toLowerCase();
          if (t === "button" || t === "submit" || t === "reset") return "button";
          if (t === "checkbox") return "checkbox";
          if (t === "radio") return "radio";
          if (t === "email" || t === "password" || t === "search" || t === "tel" || t === "url" || t === "text") return "textbox";
          return "input";
        }
        return "";
      };

      const nodes = Array.from(document.querySelectorAll(selectors.join(",")));
      const output = [];
      for (const node of nodes) {
        if (output.length >= maxElements) break;
        if (!isVisible(node)) continue;
        const role = (node.getAttribute("role") || semanticRole(node) || "").trim().toLowerCase();
        if (!role) continue;
        const text = String(node.innerText || node.textContent || node.value || "").replace(/\\s+/g, " ").trim().slice(0, 200);
        const ariaLabel = String(node.getAttribute("aria-label") || node.getAttribute("name") || "").trim().slice(0, 200);
        output.push({
          role,
          text,
          aria_label: ariaLabel
        });
      }
      return output;
    }
    """
    values = page.evaluate(script, MAX_SNAPSHOT_ELEMENTS)
    if not isinstance(values, list):
        return []
    elements: list[dict[str, str]] = []
    for value in values:
        if not isinstance(value, dict):
            continue
        role = str(value.get("role", "")).strip()
        text = str(value.get("text", "")).strip()
        aria_label = str(value.get("aria_label", "")).strip()
        if not role:
            continue
        elements.append({"role": role, "text": text, "aria_label": aria_label})
    return elements


def run_action(arguments: dict[str, Any], state: dict[str, Any]) -> Any:
    from playwright.sync_api import TimeoutError as PlaywrightTimeoutError
    from playwright.sync_api import sync_playwright

    action = canonical_action(get_optional_str(arguments, "action"))
    timeout_ms = get_timeout_ms(arguments)
    headless = get_bool(arguments, "headless", False)

    if action == "close":
        return "closed browser context"

    ensure_state_dir()

    with sync_playwright() as playwright:
        context = playwright.chromium.launch_persistent_context(
            user_data_dir=str(PROFILE_DIR),
            headless=headless,
            viewport={"width": 1400, "height": 1000},
        )
        try:
            page = context.pages[-1] if context.pages else context.new_page()
            return _run_page_action(arguments, state, page, action, timeout_ms)
        except PlaywrightTimeoutError as exc:
            raise RuntimeError(f"Timeout waiting for element or page state: {exc}") from exc
        finally:
            context.close()


def process_one_call() -> None:
    """Read one call from stdin, execute it, and write the result to stdout."""
    call = read_call()
    arguments = get_arguments(call)
    state = load_state()
    result = run_action(arguments, state)
    save_state(state)
    write_result(result=result)


def run_daemon() -> None:
    """Daemon mode: keep the process alive and handle one call per stdin line."""
    from playwright.sync_api import sync_playwright

    ensure_playwright_browser()
    ensure_state_dir()
    state = load_state()

    with sync_playwright() as playwright:
        context = playwright.chromium.launch_persistent_context(
            user_data_dir=str(PROFILE_DIR),
            headless=False,
            viewport={"width": 1400, "height": 1000},
        )
        try:
            page = context.pages[-1] if context.pages else context.new_page()
            for raw_line in sys.stdin:
                raw_line = raw_line.strip()
                if not raw_line:
                    continue
                try:
                    call = json.loads(raw_line)
                    if not isinstance(call, dict):
                        raise ValueError("tool call must be a JSON object")
                    arguments = get_arguments(call)
                    result = _run_action_with_context(arguments, state, context, page)
                    page = context.pages[-1] if context.pages else context.new_page()
                    save_state(state)
                    _write_line_result(result=result)
                except Exception as exc:  # noqa: BLE001
                    _write_line_result(error=str(exc))
        finally:
            context.close()


def _run_action_with_context(arguments: dict[str, Any], state: dict[str, Any], context: Any, page: Any) -> Any:
    """Validate a daemon call without changing its existing context lifetime."""
    action = canonical_action(get_optional_str(arguments, "action"))
    timeout_ms = get_timeout_ms(arguments)
    return _run_page_action(arguments, state, page, action, timeout_ms)


def _run_page_action(arguments: dict[str, Any], state: dict[str, Any], page: Any, action: str, timeout_ms: int) -> Any:
    """Shared action engine; mode wrappers own launch, validation and cleanup."""
    from playwright.sync_api import Error as PlaywrightError
    from playwright.sync_api import TimeoutError as PlaywrightTimeoutError

    page.set_default_timeout(timeout_ms)

    if action == "close":
        return "closed browser context"

    if action == "open":
        target = build_target(
            get_optional_str(arguments, "url"),
            get_optional_str(arguments, "search"),
            default_blank=True,
        )
        page.goto(target, wait_until="domcontentloaded", timeout=timeout_ms)
        state["last_url"] = page.url
        return {"action": "open", "url": page.url, "title": page.title()}

    maybe_navigate(page, arguments, state, timeout_ms)

    if action == "click":
        locator = pick_locator(page, arguments)
        locator.click(timeout=timeout_ms)
        try:
            page.wait_for_load_state("domcontentloaded", timeout=timeout_ms)
        except PlaywrightTimeoutError:
            pass
        state["last_url"] = page.url
        return {"action": "click", "url": page.url, "title": page.title()}

    if action == "fill":
        if "value" not in arguments:
            raise ValueError("Missing value for fill.")
        value = arguments["value"]
        if not isinstance(value, str):
            raise ValueError("value must be a string")
        locator = pick_locator(page, arguments)
        locator.fill(value, timeout=timeout_ms)
        state["last_url"] = page.url
        return {"action": "fill", "url": page.url, "title": page.title()}

    if action == "press":
        key = get_optional_str(arguments, "key")
        if not key:
            raise ValueError("Missing key for press.")
        page.keyboard.press(key)
        state["last_url"] = page.url
        return {"action": "press", "key": key, "url": page.url, "title": page.title()}

    if action == "read":
        try:
            text = page.inner_text("body", timeout=timeout_ms)
        except PlaywrightError:
            text = page.evaluate("() => document.body ? (document.body.innerText || '') : ''")
        state["last_url"] = page.url
        return {
            "action": "read",
            "url": page.url,
            "title": page.title(),
            "text": truncated_text(str(text)),
        }

    if action == "snapshot":
        elements = snapshot_elements(page)
        state["last_url"] = page.url
        return {
            "action": "snapshot",
            "url": page.url,
            "title": page.title(),
            "elements": elements,
        }

    if action == "wait":
        locator = pick_locator(page, arguments)
        locator.wait_for(state="visible", timeout=timeout_ms)
        state["last_url"] = page.url
        return {
            "action": "wait",
            "matched": True,
            "url": page.url,
            "title": page.title(),
        }

    raise ValueError(f"Unknown action: {action}")


def _write_line_result(result: Any = None, error: str | None = None) -> None:
    """Write a single newline-terminated JSON result for daemon mode."""
    write_result(result=result, error=error, end="\n")
    sys.stdout.flush()


def main() -> None:
    if "--daemon" in sys.argv:
        run_daemon()
        return
    try:
        ensure_playwright_browser()

        process_one_call()
    except Exception as exc:  # noqa: BLE001
        write_result(error=str(exc))


if __name__ == "__main__":
    main()
