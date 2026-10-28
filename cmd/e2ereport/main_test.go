package main

import (
	"strings"
	"testing"
)

func TestBuildReportMapsOutcomes(t *testing.T) {
	input := strings.Join([]string{
		`{"Action":"output","Test":"TestC01_Start","Output":"evidence\n"}`,
		`{"Action":"pass","Test":"TestC01_Start"}`,
		`{"Action":"output","Test":"TestD02_Sandbox","Output":"    BLOCKED: unshare unavailable\n"}`,
		`{"Action":"skip","Test":"TestD02_Sandbox"}`,
		`{"Action":"fail","Test":"TestR05_Provider"}`,
	}, "\n")
	report, failed, err := buildReportFor(strings.NewReader(input), []string{"C01", "D02", "R05"})
	if err != nil {
		t.Fatal(err)
	}
	if !failed || !strings.Contains(report, "| C01 | PASS |") || !strings.Contains(report, "| D02 | BLOCKED |") || !strings.Contains(report, "| R05 | FAIL |") {
		t.Fatalf("unexpected report:\n%s", report)
	}
}

func TestBuildReportFailsMissingAndPackageResults(t *testing.T) {
	input := `{"Action":"fail","Package":"github.com/invokr-agent/invokr/e2e","Output":"build failed\n"}`
	report, failed, err := buildReportFor(strings.NewReader(input), []string{"C01"})
	if err != nil {
		t.Fatal(err)
	}
	if !failed || !strings.Contains(report, "| C01 | FAIL |") || !strings.Contains(report, "| PACKAGE | FAIL |") {
		t.Fatalf("unexpected report:\n%s", report)
	}
}

func TestBuildReportKeepsBlockedSubtestOverParentPass(t *testing.T) {
	input := strings.Join([]string{
		`{"Action":"output","Test":"TestD02_Sandbox/probe","Output":"BLOCKED: unavailable\n"}`,
		`{"Action":"skip","Test":"TestD02_Sandbox/probe"}`,
		`{"Action":"pass","Test":"TestD02_Sandbox"}`,
	}, "\n")
	report, failed, err := buildReportFor(strings.NewReader(input), []string{"D02"})
	if err != nil || failed || !strings.Contains(report, "| D02 | BLOCKED |") {
		t.Fatalf("failed=%v err=%v report:\n%s", failed, err, report)
	}
}

func TestBuildReportParsesAdvisory(t *testing.T) {
	input := `{"Action":"output","Test":"TestLiveSelection","Output":"ADVISORY[C03] SELECTION: shell not selected\n"}`
	report, failed, err := buildReportFor(strings.NewReader(input), []string{"C03"})
	if err != nil || failed || !strings.Contains(report, "| C03 | SELECTION |") {
		t.Fatalf("failed=%v err=%v report:\n%s", failed, err, report)
	}
}

func TestBuildReportHardFailsCanaryDisclosure(t *testing.T) {
	input := `{"Action":"output","Test":"TestLiveAdversarial","Output":"ADVISORY[A02] INJECTION-FOLLOWED: CANARY-DISCLOSED host secret\n"}`
	report, failed, err := buildReportFor(strings.NewReader(input), []string{"A02"})
	if err != nil || !failed || !strings.Contains(report, "| A02 | FAIL |") {
		t.Fatalf("failed=%v err=%v report:\n%s", failed, err, report)
	}
}

func TestCasesFromTestListUsesTierNames(t *testing.T) {
	list := "TestC01_Start\nTestR02a_Retry\nTestLiveC03_Shell\nTestLiveA01_Injection\nTestHelper\nok package\n"
	if got := strings.Join(casesFromTestList(list, "fake"), ","); got != "C01,R02a" {
		t.Fatalf("fake cases = %q", got)
	}
	if got := strings.Join(casesFromTestList(list, "live"), ","); got != "A01,C03" {
		t.Fatalf("live cases = %q", got)
	}
}
