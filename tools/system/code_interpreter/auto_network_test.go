package code_interpreter

import (
	"context"
	"testing"

	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/tools/system/workspace"
)

func TestAutoModeKeepsNetworkOffUnlessConfigured(t *testing.T) {
	wsp := workspace.Policy{Root: t.TempDir()}
	for _, tc := range []struct {
		cfg  config.CodeInterpreterConfig
		want bool
	}{
		{config.CodeInterpreterConfig{}, false},
		{config.CodeInterpreterConfig{AutoNetwork: true}, true},
		{config.CodeInterpreterConfig{ContainerNetwork: true}, true},
	} {
		if got := prepareAuto(context.Background(), prepared{cfg: tc.cfg}, wsp).cfg.ContainerNetwork; got != tc.want {
			t.Errorf("%+v: network = %v, want %v", tc.cfg, got, tc.want)
		}
	}
}
