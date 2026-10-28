package agent

import (
	"context"
	"testing"

	"github.com/invokr-agent/invokr/clients"
	inference "github.com/rivt-ai/go-inference-router"
)

func TestRunnerForwardsImagesToModel(t *testing.T) {
	runner := testRunner(t, okHandler, []string{`{"type":"reply","message":"a cat"}`})
	image := inference.ImageData("image/png", []byte{1})
	if _, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "what is this?", Images: []inference.ContentBlock{image}}); err != nil {
		t.Fatal(err)
	}
	begin := runner.modelGateway.(*sequenceGateway).beginRequests
	if len(begin) != 1 || len(begin[0].Images) != 1 || begin[0].Images[0].Media.MIMEType != "image/png" {
		t.Fatalf("begin requests = %#v, want the image forwarded", begin)
	}
}

func TestProviderContextAcceptsImagesUnlessServerSaysNo(t *testing.T) {
	c := NewProviderContext(0, "m")
	if !c.AcceptsImages() {
		t.Fatal("unknown capability must not block images")
	}
	c.ApplyMetadata("m", clients.ProviderMetadata{NoVision: true})
	if c.AcceptsImages() {
		t.Fatal("modalities.vision=false must block images")
	}
}
