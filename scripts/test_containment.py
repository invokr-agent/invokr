import unittest

from containment import failures


class ContainmentGateTests(unittest.TestCase):
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
