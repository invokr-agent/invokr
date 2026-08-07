package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/platform/events"
	inference "github.com/rivt-ai/go-inference-router"
	"go.uber.org/zap"
)

func TestCompactionRestoresCurrentTodoOnlyAfterRewrite(t *testing.T) {
	for _, mode := range []string{"summary", "fallback", "over-window", "no-reduction", "empty"} {
		t.Run(mode, func(t *testing.T) {
			gateway := &compactionFlowGateway{}
			if mode == "fallback" || mode == "no-reduction" {
				gateway.summaryErr = errors.New("unavailable")
			}
			list := "- [ ] current task\n"
			if mode == "empty" {
				list = ""
			}
			reads := 0
			budget := &contextBudget{
				gateway: gateway, windowSize: 10000, logger: zap.NewNop(), events: events.NewOrderedDispatcher(zap.NewNop()),
				todo: func() string { reads++; return list },
			}
			messages := replanMessages(20)
			if mode == "fallback" || mode == "over-window" {
				messages = []inference.Message{inference.SystemMessage("system"), inference.UserMessage("start")}
				for _, id := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
					messages = append(messages, exchange(id, "shell", `{}`, strings.Repeat("output", 1000))...)
				}
			}
			if mode == "no-reduction" {
				messages = messages[:2]
			}
			percent := int64(90)
			if mode == "over-window" {
				percent = 110
			}
			plan := planRetention(messages, 0, 1, nil)
			got, state := budget.compact(context.Background(), "s", messages, plan, contextWindowState{}, 10000, percent)
			if got[0].Content != "system" {
				t.Fatal("system prefix changed")
			}
			wantSnapshot := mode != "no-reduction" && mode != "empty"
			last := got[len(got)-1]
			hasSnapshot := list != "" && strings.Contains(last.Content, list)
			if hasSnapshot != wantSnapshot {
				t.Fatalf("snapshot presence mismatch: last=%q", last.Content)
			}
			if wantSnapshot && last.Role != inference.RoleUser {
				t.Fatal("snapshot must be a user message")
			}
			if mode == "no-reduction" && reads != 0 {
				t.Fatal("no-op compaction read todo state")
			}
			// A normal turn must neither append another snapshot nor refresh the old one.
			beforeReads := reads
			list = "- [x] current task\n"
			budget.windowSize = 1000000
			again, _ := budget.Prepare(context.Background(), "s", got, state, nil)
			if reads != beforeReads || !reflect.DeepEqual(again, got) {
				t.Fatal("ordinary turn refreshed todo state")
			}
		})
	}
}
