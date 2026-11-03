package setup

import (
	"io"
	"os"
	"testing"
)

// Installing the default skills clones the skill hub over the network, ~4s per
// questionnaire run; these tests cover the questionnaire, not the hub.
func TestMain(m *testing.M) {
	installDefaultSkills = func(string, io.Writer) {}
	os.Exit(m.Run())
}
