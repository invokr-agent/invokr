import unittest

from containment import FALLBACK_CASES, FALLBACK_REQUIRED, REQUIRED, failures


class ContainmentGateTests(unittest.TestCase):
    def test_namespace_unavailable_stage_is_mandatory_and_fail_closed(self):
        required = {("shell", name) for name in [FALLBACK_REQUIRED] + [FALLBACK_REQUIRED + "/" + c for c in FALLBACK_CASES]}
        self.assertEqual(FALLBACK_REQUIRED, "TestShellNamespaceUnavailableFallback")
        self.assertEqual(len(FALLBACK_CASES), 6)
        passed = [{"Package": "shell", "Test": name, "Action": "pass"} for _, name in sorted(required)]
        self.assertEqual(failures(passed, required), [])
        for missing in required:
            with self.subTest(missing=missing):
                self.assertTrue(failures([e for e in passed if e["Test"] != missing[1]], required))
        for action in ("skip", "fail"):
            with self.subTest(action=action):
                events = passed + [{
                    "Package": "shell", "Test": FALLBACK_REQUIRED + "/control", "Action": action,
                }]
                self.assertTrue(failures(events, required))

    def test_git_mount_boundary_tests_are_mandatory_and_fail_closed(self):
        names = {
            "TestGitPointerCannotExpandSandboxWritableMounts",
            "TestGitMountSourcePinnedAcrossPathReplacement",
            "TestGitPinDescriptorClosedBeforePayload",
        }
        self.assertFalse(names - set(REQUIRED["tools/system/shell"]))
        required = {("shell", name) for name in names}
        passed = [{"Package": "shell", "Test": name, "Action": "pass"} for name in names]
        self.assertEqual(failures(passed, required), [])
        for action in ("missing", "skip", "fail"):
            with self.subTest(action=action):
                events = passed[1:] if action == "missing" else passed + [{
                    "Package": "shell", "Test": passed[0]["Test"] + "/subcase", "Action": action,
                }]
                self.assertTrue(failures(events, required))

    def test_missing_and_skipped_tests_cannot_make_a_green_gate(self):
        required = {("sandbox", "TestBoundary")}
        self.assertTrue(failures([], required))
        self.assertTrue(failures([{"Package": "sandbox", "Action": "pass"}], required))
        self.assertTrue(failures([
            {"Package": "sandbox", "Test": "TestBoundary", "Action": "skip"},
            {"Package": "sandbox", "Action": "pass"},
        ], required))

    def test_passing_parent_does_not_hide_skipped_child(self):
        events = [
            {"Package": "sandbox", "Test": "TestBoundary", "Action": "pass"},
            {"Package": "sandbox", "Test": "TestBoundary/escape", "Action": "skip"},
        ]
        self.assertTrue(failures(events, {("sandbox", "TestBoundary")}))
        self.assertEqual(failures(events[:1], {("sandbox", "TestBoundary")}), [])


if __name__ == "__main__":
    unittest.main()
