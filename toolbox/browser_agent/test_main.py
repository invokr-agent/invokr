"""Browser orchestration contracts; no Playwright installation or network needed."""
import importlib.util
import io
import json
import sys
import types
import unittest
from pathlib import Path
from unittest.mock import Mock, patch

spec = importlib.util.spec_from_file_location("browser_agent_under_test", Path(__file__).with_name("main.py"))
browser = importlib.util.module_from_spec(spec)
spec.loader.exec_module(browser)


class BrowserError(Exception):
    pass


class BrowserTimeout(BrowserError):
    pass


class Page:
    """Small explicit fake: unexpected browser methods fail rather than auto-mock."""
    def __init__(self, url="https://example.test/"):
        self.url = url
        self.calls = []
        self.failures = {}
        self.text = "page body"
        self.fallback = "fallback body"
        self.elements = [{"role": "button", "text": "Go", "aria_label": "Go"}]
        self.keyboard = self
        self.first = self

    def record(self, method, *args, **kwargs):
        self.calls.append((method, args, kwargs))
        if method in self.failures:
            raise self.failures[method]

    def set_default_timeout(self, timeout):
        self.record("timeout", timeout)

    def title(self):
        self.record("title")
        return "Example"

    def goto(self, target, *, wait_until, timeout):
        self.record("goto", target, wait_until=wait_until, timeout=timeout)
        self.url = target

    def get_by_role(self, role, **kwargs):
        self.record("role", role, **kwargs)
        return self

    def get_by_label(self, label):
        self.record("label", label)
        return self

    def get_by_placeholder(self, placeholder):
        self.record("placeholder", placeholder)
        return self

    def get_by_text(self, text):
        self.record("text", text)
        return self

    def locator(self, selector):
        self.record("selector", selector)
        return self

    def click(self, *, timeout):
        self.record("click", timeout=timeout)

    def fill(self, value, *, timeout):
        self.record("fill", value, timeout=timeout)

    def press(self, key):
        self.record("press", key)

    def wait_for(self, *, state, timeout):
        self.record("wait_for", state=state, timeout=timeout)

    def wait_for_load_state(self, state, *, timeout):
        self.record("load_state", state, timeout=timeout)

    def inner_text(self, selector, *, timeout):
        self.record("inner_text", selector, timeout=timeout)
        return self.text

    def evaluate(self, script, *args):
        self.record("evaluate", script, *args)
        return self.elements if args else self.fallback


class BrowserContracts(unittest.TestCase):
    def setUp(self):
        self.page = Page()
        self.context = Mock(spec=["pages", "new_page", "close"])
        self.context.pages = [self.page]
        self.context.new_page.return_value = self.page
        self.launch = Mock(return_value=self.context)
        api = types.ModuleType("playwright.sync_api")
        api.Error, api.TimeoutError = BrowserError, BrowserTimeout
        driver = Mock(chromium=Mock(launch_persistent_context=self.launch))
        manager = Mock(__enter__=Mock(return_value=driver), __exit__=Mock(return_value=False))
        api.sync_playwright = Mock(return_value=manager)
        package = types.ModuleType("playwright")
        package.sync_api = api
        self.enter(patch.dict(sys.modules, {"playwright": package, "playwright.sync_api": api}))
        self.ensure_dir = self.enter(patch.object(browser, "ensure_state_dir"))
        self.ensure_browser = self.enter(patch.object(browser, "ensure_playwright_browser"))
        self.saved = []
        self.enter(patch.object(browser, "save_state", side_effect=lambda state: self.saved.append(dict(state))))
        self.enter(patch.object(browser, "load_state", return_value={}))

    def enter(self, patcher):
        value = patcher.start()
        self.addCleanup(patcher.stop)
        return value

    def invoke(self, mode, arguments, state):
        if mode == "one-shot":
            return browser.run_action(arguments, state)
        return browser._run_action_with_context(arguments, state, self.context, self.page)

    def daemon(self, lines):
        output = io.StringIO()
        with patch.object(sys, "stdin", io.StringIO(lines)), patch.object(sys, "stdout", output):
            browser.run_daemon()
        return [json.loads(line) for line in output.getvalue().splitlines()]

    def test_actions_have_identical_payloads_state_and_browser_calls(self):
        cases = [
            ({"action": "open", "url": "example.test"}, {"action": "open", "url": "https://example.test", "title": "Example"}),
            ({"action": "click", "role": "button", "name": "Go"}, {"action": "click", "url": "https://example.test/", "title": "Example"}),
            ({"action": "fill", "selector": "input", "value": "  exact value  "}, {"action": "fill", "url": "https://example.test/", "title": "Example"}),
            ({"action": "press", "key": " Enter "}, {"action": "press", "key": "Enter", "url": "https://example.test/", "title": "Example"}),
            ({"action": "read"}, {"action": "read", "url": "https://example.test/", "title": "Example", "text": "page body"}),
            ({"action": "snapshot"}, {"action": "snapshot", "url": "https://example.test/", "title": "Example", "elements": self.page.elements}),
            ({"action": "wait", "label": "Name"}, {"action": "wait", "matched": True, "url": "https://example.test/", "title": "Example"}),
        ]
        for arguments, expected in cases:
            traces = []
            for mode in ("one-shot", "daemon"):
                with self.subTest(action=arguments["action"], mode=mode):
                    self.page.url, self.page.calls = "https://example.test/", []
                    state = {}
                    result = self.invoke(mode, {**arguments, "timeout_ms": 1234}, state)
                    self.assertEqual(json.dumps(result), json.dumps(expected))
                    self.assertEqual(state, {"last_url": expected["url"]})
                    self.assertEqual(self.page.calls[0], ("timeout", (1234,), {}))
                    traces.append(list(self.page.calls))
            self.assertEqual(traces[0], traces[1])
        self.assertEqual(self.context.close.call_count, len(cases))

    def test_aliases(self):
        aliases = {
            "open": ["", "open", "open-page", "navigate", "goto", "go to"],
            "click": ["click", "click_element", " TAP "],
            "fill": ["fill", "fill_input", "type", "enter_text"],
            "press": ["press", "press_key", "key_press"],
            "read": ["read", "read_page", "get_text"],
            "snapshot": ["snapshot", "snapshot_page", "inspect"],
            "wait": ["wait", "wait_for", "wait_for_element"],
            "close": ["close", "close_browser", "close_context"],
        }
        for action, names in aliases.items():
            for name in names:
                with self.subTest(alias=name):
                    self.assertEqual(browser.canonical_action(name), action)
        self.assertEqual(browser.canonical_action(" Other-Action "), "other_action")

    def test_validation_errors_and_cleanup(self):
        cases = [
            ({"action": "fill", "selector": "input"}, "Missing value for fill."),
            ({"action": "fill", "value": 1}, "value must be a string"),
            ({"action": "press"}, "Missing key for press."),
            ({"action": "click"}, "No target locator provided."),
            ({"action": "open", "url": "a", "search": "b"}, "provide either url or search"),
            ({"action": "missing"}, "Unknown action: missing"),
        ]
        for args, message in cases:
            for mode in ("one-shot", "daemon"):
                with self.subTest(args=args, mode=mode):
                    self.context.close.reset_mock()
                    with self.assertRaises(ValueError) as raised:
                        self.invoke(mode, args, {})
                    self.assertIn(message, str(raised.exception))
                    self.assertEqual(self.context.close.call_count, int(mode == "one-shot"))

    def test_prelaunch_validation_and_mode_specific_headless(self):
        for timeout in (True, "10", 0, -1, float("nan"), float("inf")):
            with self.subTest(timeout=timeout):
                with self.assertRaises(ValueError):
                    browser.run_action({"action": "close", "timeout_ms": timeout}, {})
        with self.assertRaisesRegex(ValueError, "headless must be a boolean"):
            browser.run_action({"action": "close", "headless": "yes"}, {})
        self.launch.assert_not_called()
        self.assertEqual(browser.run_action({"action": "close"}, {}), "closed browser context")
        self.ensure_dir.assert_not_called()
        self.assertEqual(self.invoke("daemon", {"action": "close", "headless": "ignored"}, {}), "closed browser context")
        self.context.close.assert_not_called()
        self.assertEqual(self.page.calls, [("timeout", (10000,), {})])
        browser.run_action({"action": "read", "headless": True}, {})
        self.assertTrue(self.launch.call_args.kwargs["headless"])

    def test_timeout_wrapping_is_mode_specific(self):
        self.page.failures["wait_for"] = BrowserTimeout("late")
        with self.assertRaisesRegex(RuntimeError, "Timeout waiting for element or page state: late") as raised:
            browser.run_action({"action": "wait", "selector": "button"}, {})
        self.assertIsInstance(raised.exception.__cause__, BrowserTimeout)
        with self.assertRaisesRegex(BrowserTimeout, "^late$"):
            self.invoke("daemon", {"action": "wait", "selector": "button"}, {})
        self.context.close.assert_called_once()

    def test_launch_failure_is_not_wrapped(self):
        self.launch.side_effect = BrowserTimeout("launch failed")
        with self.assertRaisesRegex(BrowserTimeout, "^launch failed$"):
            browser.run_action({"action": "read"}, {})
        self.context.close.assert_not_called()

    def test_click_navigation_timeout_is_ignored_but_other_errors_propagate(self):
        for mode in ("one-shot", "daemon"):
            self.page.failures["load_state"] = BrowserTimeout("navigation")
            self.assertEqual(self.invoke(mode, {"action": "click", "selector": "a"}, {})["action"], "click")
            self.page.failures["load_state"] = BrowserError("other")
            with self.assertRaisesRegex(BrowserError, "other"):
                self.invoke(mode, {"action": "click", "selector": "a"}, {})

    def test_read_fallback_and_truncation(self):
        for mode in ("one-shot", "daemon"):
            self.page.failures["inner_text"] = BrowserError("no body")
            self.page.fallback = "x" * (browser.MAX_READ_CHARS + 5)
            result = self.invoke(mode, {"action": "read"}, {})
            self.assertEqual(result["text"], "x" * browser.MAX_READ_CHARS + "\n...[truncated]")

    def test_locator_precedence_still_validates_all_fields(self):
        args = {"role": "button", "name": "Go", "label": "Label", "placeholder": "Hint", "text": "Text", "selector": "a"}
        for chosen in ("role", "label", "placeholder", "text", "selector"):
            self.page.calls = []
            self.assertIs(browser.pick_locator(self.page, args), self.page)
            self.assertEqual(self.page.calls[0][0], chosen)
            args.pop(chosen)
        with self.assertRaisesRegex(ValueError, "selector must be a string"):
            browser.pick_locator(self.page, {"role": "button", "selector": 12})

    def test_navigation_restore_and_unknown_action_order(self):
        for mode in ("one-shot", "daemon"):
            self.page.url, self.page.calls = "about:blank", []
            result = self.invoke(mode, {"action": "read"}, {"last_url": " https://restore.test "})
            self.assertEqual(result["url"], "https://restore.test")
            self.assertEqual(self.page.calls[1][0], "goto")
            state = {}
            with self.assertRaisesRegex(ValueError, "Unknown action"):
                self.invoke(mode, {"action": "missing", "url": "next.test"}, state)
            self.assertEqual(state["last_url"], "https://next.test")

    def test_daemon_keeps_one_context_continues_after_errors_and_saves_successes(self):
        requests = '\ninvalid\n[]\n' + '\n'.join(json.dumps({"arguments": args}) for args in (
            {"action": "open", "url": "first.test"},
            {"action": "fill"},
            {"action": "close"},
            {"action": "read", "headless": "ignored"},
        ))
        results = self.daemon(requests)
        self.assertEqual(len(results), 6)
        self.assertIn("error", results[0])
        self.assertEqual(results[1], {"error": "tool call must be a JSON object"})
        self.assertEqual(json.loads(results[2]["result"])["url"], "https://first.test")
        self.assertEqual(results[3], {"error": "Missing value for fill."})
        self.assertEqual(results[4], {"result": "closed browser context"})
        self.assertEqual(json.loads(results[5]["result"])["action"], "read")
        self.assertEqual(len(self.saved), 3)
        self.launch.assert_called_once_with(user_data_dir=str(browser.PROFILE_DIR), headless=False, viewport={"width": 1400, "height": 1000})
        self.context.close.assert_called_once()

    def test_daemon_reselects_page_and_creates_missing_page(self):
        second = Page("https://second.test/")
        self.context.pages = []
        def new_page():
            self.context.pages = [self.page, second]
            return self.page
        self.context.new_page.side_effect = new_page
        results = self.daemon('\n'.join(json.dumps({"arguments": {"action": "read"}}) for _ in range(2)))
        self.assertEqual(json.loads(results[0]["result"])["url"], "https://example.test/")
        self.assertEqual(json.loads(results[1]["result"])["url"], "https://second.test/")
        self.context.new_page.assert_called_once()

    def test_one_shot_creates_missing_page(self):
        self.context.pages = []
        self.assertEqual(browser.run_action({"action": "read"}, {})["text"], "page body")
        self.context.new_page.assert_called_once()
        self.context.close.assert_called_once()

    def test_result_framing(self):
        for value in (None, False, 0, "", "é", {"a": [1, 2]}):
            for error in (None, "", "failed"):
                for daemon in (False, True):
                    with self.subTest(value=value, error=error, daemon=daemon):
                        output = Mock(spec=["write", "flush"])
                        expected = {}
                        if value is not None:
                            expected["result"] = value if isinstance(value, str) else json.dumps(value)
                        if error:
                            expected["error"] = error
                        with patch.object(sys, "stdout", output):
                            writer = browser._write_line_result if daemon else browser.write_result
                            writer(result=value, error=error)
                        output.write.assert_called_once_with(json.dumps(expected) + ("\n" if daemon else ""))
                        self.assertEqual(output.flush.call_count, int(daemon))

    def test_main_success_and_failure_framing(self):
        for args in ({"action": "read"}, {"action": "fill"}):
            output = io.StringIO()
            with patch.object(sys, "argv", ["main.py"]), patch.object(sys, "stdin", io.StringIO(json.dumps({"arguments": args}))), patch.object(sys, "stdout", output):
                browser.main()
            raw = output.getvalue()
            self.assertFalse(raw.endswith("\n"))
            if args["action"] == "read":
                self.assertEqual(json.loads(json.loads(raw)["result"])["text"], "page body")
            else:
                self.assertEqual(json.loads(raw), {"error": "Missing value for fill."})
        self.assertEqual(len(self.saved), 1)
        self.assertEqual(self.ensure_browser.call_count, 2)

    def test_main_daemon_mode_uses_daemon_lifecycle(self):
        output = io.StringIO()
        with patch.object(sys, "argv", ["main.py", "--daemon"]), patch.object(sys, "stdin", io.StringIO('{"arguments":{"action":"read"}}\n')), patch.object(sys, "stdout", output):
            browser.main()
        self.assertTrue(output.getvalue().endswith("\n"))
        self.assertEqual(json.loads(json.loads(output.getvalue())["result"])["action"], "read")
        self.ensure_browser.assert_called_once()


if __name__ == "__main__":
    unittest.main()
