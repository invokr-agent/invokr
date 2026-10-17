package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	inference "github.com/rivt-ai/go-inference-router"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestOverflowLogPreservesRequestAndPartialResponse(t *testing.T) {
	thought := strings.Repeat("partial reasoning ", 16384)
	cut := lengthCutResponse("")
	cut.Message.Reasoning = thought
	cut.Usage.CompletionTokens = 16384
	for _, tc := range []struct {
		name     string
		response *inference.Response
		err      error
		wantLog  bool
		level    zapcore.Level
	}{
		{"length", cut, nil, true, zap.DebugLevel},
		{"parse rejection", nil, &inference.Error{Kind: inference.KindToolCallParse, Message: "truncated tool arguments"}, true, zap.DebugLevel},
		{"normal", &inference.Response{Message: inference.AssistantMessage("ok")}, nil, false, zap.DebugLevel},
		{"debug disabled", cut, nil, false, zap.InfoLevel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &scriptedProvider{outputs: []*inference.Response{tc.response}, errors: []error{tc.err}}
			gateway := testModelGateway(provider)
			var log bytes.Buffer
			gateway.logger = zap.New(zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&log), tc.level))
			messages := []inference.Message{inference.SystemMessage("full system"), inference.UserMessage("full task")}
			_, _ = gateway.completeAttempt(context.Background(), messages, "s1", false, false)
			var snapshotLine []byte
			for _, line := range bytes.Split(log.Bytes(), []byte("\n")) {
				if bytes.Contains(line, []byte(`"msg":"completion overflow snapshot"`)) {
					snapshotLine = line
				}
			}
			if !tc.wantLog {
				if len(snapshotLine) != 0 {
					t.Fatalf("normal response logged as overflow: %s", log.String())
				}
				return
			}
			var snapshot struct {
				Message  string              `json:"msg"`
				Request  inference.Request   `json:"request"`
				Response *inference.Response `json:"response"`
				Error    string              `json:"error"`
			}
			if err := json.Unmarshal(snapshotLine, &snapshot); err != nil {
				t.Fatal(err)
			}
			if snapshot.Message != "completion overflow snapshot" || len(snapshot.Request.Messages) != 2 || snapshot.Request.Messages[0].Content != "full system" || snapshot.Request.Messages[1].Content != "full task" {
				t.Fatalf("incomplete request snapshot: %#v", snapshot.Request)
			}
			if tc.response != nil && (snapshot.Response == nil || snapshot.Response.Message.Reasoning != thought || snapshot.Response.Usage.CompletionTokens != 16384) {
				t.Fatal("overflow response or reasoning truncated in log")
			}
			if tc.err != nil && (snapshot.Response != nil || snapshot.Error == "") {
				t.Fatal("missing parse rejection details")
			}
		})
	}
}

func TestAuxiliaryOverflowSnapshots(t *testing.T) {
	for _, summary := range []bool{false, true} {
		name := "continuation"
		if summary {
			name = "summary"
		}
		t.Run(name, func(t *testing.T) {
			cut := lengthCutResponse("partial output")
			cut.Message.Reasoning = strings.Repeat("unfinished thought ", 16384)
			gateway := testModelGateway(&scriptedProvider{outputs: []*inference.Response{cut}})
			core, logs := observer.New(zap.DebugLevel)
			gateway.logger = zap.New(core)
			if summary {
				_, _ = gateway.SummarizeConversation(context.Background(), []inference.Message{inference.UserMessage("preserve this task")})
			} else {
				gateway.continueToolCall(context.Background(), []inference.Message{inference.UserMessage("preserve this task")}, completionFromResponse(truncatedCallResponse(`{"path":"a`)))
			}
			snapshots := logs.FilterMessage("completion overflow snapshot").All()
			if len(snapshots) != 1 {
				t.Fatalf("got %d snapshots", len(snapshots))
			}
			fields := snapshots[0].ContextMap()
			response, ok := fields["response"].(*inference.Response)
			if !ok || response.Message.Reasoning != cut.Message.Reasoning {
				t.Fatal("partial reasoning not preserved")
			}
			req, ok := fields["request"].(inference.Request)
			if !ok {
				t.Fatal("request not preserved")
			}
			raw, _ := json.Marshal(req)
			if !bytes.Contains(raw, []byte("preserve this task")) {
				t.Fatal("task missing from request")
			}
		})
	}
}
