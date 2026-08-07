#!/usr/bin/env python3
"""Run the mandatory Linux containment suite; missing/skipped tests fail closed."""
import json
import os
import subprocess
import sys

PREFIX = subprocess.run(["go", "list", "-m"], stdout=subprocess.PIPE, text=True, check=True).stdout.strip() + "/"
REQUIRED = {
    "tools/core/sandbox": [
        "TestSandboxCommandExecutesToolDirCommand",
        "TestSandboxCommandDeniesHostRootEscape",
        "TestSandboxCommandDeniesHostPasswd",
        "TestSandboxCommandTimeoutKillsChildProcessGroup",
        "TestSandboxChildDiesWithExternallyKilledParent",
        "TestForegroundAutoChecksNewProtectedNames",
        "TestForegroundCleanupPreservesOverlappingApprovedCreation",
        "TestCleanupDoesNotFollowReplacedProtectedParent",
        "TestExistingSealProtectsOnlyItsPath",
    ],
    "tools/system/shell": [
        "TestShellSandboxSecuritySweep",
        "TestShellNetworkAllowedReachesHostServer",
    ],
    "tools/system/code_interpreter": [
        "TestProcessBackendWriteOutsideSessionDirStaysOffHost",
        "TestProcessBackendCannotReadHostEtcPasswd",
        "TestProcessBackendWorkspaceIsReadOnlyCwd",
    ],
}


def failures(events, required):
    passed = {(e.get("Package"), e.get("Test")) for e in events if e.get("Action") == "pass"}
    errors = [f"{p}/{t}: did not pass" for p, t in sorted(required - passed)]
    for event in events:
        if event.get("Action") in ("skip", "fail"):
            errors.append(f"{event.get('Package')}/{event.get('Test', '')}: {event['Action']}")
    return errors


def main():
    required = {(PREFIX + p, t) for p, tests in REQUIRED.items() for t in tests}
    pattern = "^(" + "|".join(sorted(t for _, t in required)) + ")$"
    env = dict(os.environ, INVOKR_TOOLBOX_ALLOW_UNSANDBOXED="0")
    command = ["go", "test", "-race", "-count=1", "-json", "-timeout=10m", "-run", pattern]
    result = subprocess.run(command + ["./" + p for p in REQUIRED], env=env,
                            stdout=subprocess.PIPE, text=True)
    events = []
    for line in result.stdout.splitlines():
        event = json.loads(line)
        events.append(event)
        if event.get("Output"):
            print(event["Output"], end="")
    errors = failures(events, required)
    for error in errors:
        print(error, file=sys.stderr)
    return int(result.returncode != 0 or bool(errors))


if __name__ == "__main__":
    sys.exit(main())
