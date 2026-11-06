package approval_test

import (
	"context"
	"testing"
	"time"

	"github.com/invokr-agent/invokr/capability/approval"
)

func TestApprovalQueueMirrorSeesParkedRequestsAndAnswers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker := approval.NewBroker()
	queue := approval.NewApprovalQueue(ctx, broker)

	parked := make(chan approval.Request, 8)
	var answers []approval.Decision
	queue.SetMirror(
		func(_ string, req approval.Request) { parked <- req },
		func(_ string, decision approval.Decision) { answers = append(answers, decision) })

	go func() { _, _ = broker.RequestApproval(ctx, approval.Request{Tool: "shell"}) }()
	var first approval.Request
	select {
	case req := <-parked:
		first = req
	case <-time.After(time.Second):
		t.Fatal("a parked approval never reached the mirror")
	}

	queue.Decide(first.ID, approval.Decision{Approved: true})
	queue.Decide(first.ID, approval.Decision{Approved: false, Reason: "second answer"})
	if len(answers) != 1 {
		t.Fatalf("answers = %d, want the one answer that settled the request", len(answers))
	}
	if !answers[0].Approved {
		t.Fatalf("mirror saw %v, want the approved answer", answers[0])
	}
}

func TestMirrorReplayCarriesWhoRaisedTheRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker := approval.NewBroker()
	queue := approval.NewApprovalQueue(ctx, broker)

	raised := make(chan approval.Request, 8)
	queue.SetMirror(func(_ string, req approval.Request) { raised <- req }, nil)
	go func() { _, _ = broker.RequestApproval(ctx, approval.Request{Tool: "shell", Subject: "ls"}) }()
	go func() {
		_, _ = broker.RequestApproval(approval.Pin(ctx), approval.Request{Tool: "shell", Subject: "git push"})
	}()

	var host, contact approval.Request
	for round := 0; round < 10; round++ {
		select {
		case req := <-raised:
			if req.Subject == "ls" {
				host = req
			} else {
				contact = req
			}
		case <-time.After(200 * time.Millisecond):
		}
		if host.Subject != "" && contact.Subject != "" {
			break
		}
	}
	if host.Subject == "" || contact.Subject == "" {
		t.Fatalf("the mirror saw %q and %q, want both requests", host.Subject, contact.Subject)
	}
	if host.Pinned {
		t.Fatal("the host's own request arrived marked pinned")
	}
	if !contact.Pinned {
		t.Fatal("a pinned turn's request arrived unmarked, so the mirror cannot tell it apart")
	}
}

func TestApprovalQueueMirrorReplaysParkedRequests(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker := approval.NewBroker()
	queue := approval.NewApprovalQueue(ctx, broker)
	go func() { _, _ = broker.RequestApproval(ctx, approval.Request{Tool: "shell"}) }()
	wait := time.Now().Add(time.Second)
	for time.Now().Before(wait) && queue.Len() == 0 {
		time.Sleep(time.Millisecond)
	}
	if queue.Len() == 0 {
		t.Fatal("the request never reached the queue")
	}

	var seen []string
	queue.SetMirror(func(id string, _ approval.Request) { seen = append(seen, id) }, nil)
	if len(seen) != 1 {
		t.Fatalf("replayed %d requests, want the one already parked", len(seen))
	}
	if seen[0] == "" {
		t.Fatal("the replayed request carried no id to answer")
	}
}
