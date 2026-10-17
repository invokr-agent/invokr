#!/usr/bin/env python3
"""Run the mandatory Linux containment suite; missing/skipped tests fail closed."""
import json
import os
import subprocess
import sys
import tempfile

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
        "TestGitPointerCannotExpandSandboxWritableMounts",
        "TestGitMountSourcePinnedAcrossPathReplacement",
        "TestGitPinDescriptorClosedBeforePayload",
        "TestShellSandboxSecuritySweep",
        "TestShellNetworkAllowedReachesHostServer",
    ],
    "tools/system/code_interpreter": [
        "TestProcessBackendWriteOutsideSessionDirStaysOffHost",
        "TestProcessBackendCannotReadHostEtcPasswd",
        "TestProcessBackendWorkspaceIsReadOnlyCwd",
    ],
}
FALLBACK_REQUIRED = "TestShellNamespaceUnavailableFallback"
FALLBACK_CASES = ["DefaultRefusesScript", "AllowlistedCommandRuns", "DeniedApprovalDisclosesHost", "OutsideCwdRejected",
                  "PlanRefusesHostFallback", "ApprovedOptInRuns"]


def failures(events, required):
    passed = {(e.get("Package"), e.get("Test")) for e in events if e.get("Action") == "pass"}
    errors = [f"{p}/{t}: did not pass" for p, t in sorted(required - passed)]
    for event in events:
        if event.get("Action") in ("skip", "fail"):
            errors.append(f"{event.get('Package')}/{event.get('Test', '')}: {event['Action']}")
    return errors


def unavailable_namespace_events():
    """Start a compiled test binary after making the one-time probe fail."""
    with tempfile.TemporaryDirectory() as directory:
        binary = os.path.join(directory, "shell.test")
        subprocess.run(["go", "test", "-race", "-c", "-o", binary, "./tools/system/shell"], check=True)
        shim = os.path.join(directory, "unshare")
        with open(shim, "w", encoding="utf-8") as script:
            script.write("#!/bin/sh\nexit 77\n")
        os.chmod(shim, 0o755)
        env = dict(os.environ, PATH=directory + os.pathsep + os.environ["PATH"],
                   INVOKR_TOOLBOX_ALLOW_UNSANDBOXED="0", INVOKR_TEST_NAMESPACE_UNAVAILABLE="1")
        result = subprocess.run(
            ["go", "tool", "test2json", "-p", PREFIX + "tools/system/shell", "-t", binary,
             "-test.v", "-test.timeout=2m", "-test.run", "^" + FALLBACK_REQUIRED + "$"],
            env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
        )
        if result.stderr:
            print(result.stderr, file=sys.stderr)
        return result.returncode, [json.loads(line) for line in result.stdout.splitlines()]


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
    fallback_code, fallback_events = unavailable_namespace_events()
    for event in fallback_events:
        if event.get("Output"):
            print(event["Output"], end="")
    errors.extend(failures(fallback_events, {(PREFIX + "tools/system/shell", name) for name in
                                             [FALLBACK_REQUIRED] + [FALLBACK_REQUIRED + "/" + c for c in FALLBACK_CASES]}))
    for error in errors:
        print(error, file=sys.stderr)
    return int(result.returncode != 0 or fallback_code != 0 or bool(errors))


if __name__ == "__main__":
    sys.exit(main())
