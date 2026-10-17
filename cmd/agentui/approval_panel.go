package agentui

// approvalPanel holds the batch-approval-queue paging state: which item is
// selected and whether its details are expanded. It has no knowledge of the
// queue itself — callers pass the current queue length in.
type approvalPanel struct {
	cursor       int // index of selected item (0-based); clamped to [0, len-1]
	expanded     bool
	detailOffset int
}

// Up moves the cursor to the previous item, collapsing any expanded details.
func (p *approvalPanel) Up() {
	if p.cursor > 0 {
		p.cursor--
		p.Collapse()
	}
}

// Down moves the cursor to the next item (n is the current queue length),
// collapsing any expanded details.
func (p *approvalPanel) Down(n int) {
	if p.cursor < n-1 {
		p.cursor++
		p.Collapse()
	}
}

// Toggle flips whether the selected item's details are expanded.
func (p *approvalPanel) Toggle() { p.expanded, p.detailOffset = !p.expanded, 0 }

func (p *approvalPanel) Collapse() { p.expanded, p.detailOffset = false, 0 }

func (p *approvalPanel) Scroll(delta, total int) {
	maxOffset := max(0, total-approvalDetailHeight)
	p.detailOffset = min(max(0, p.detailOffset+delta), maxOffset)
}

func (p *approvalPanel) ScrollToBottom(total int) {
	p.detailOffset = max(0, total-approvalDetailHeight)
}

// Clamp keeps the cursor within [0, n-1], resetting to 0 when the queue is
// empty. Call after the underlying queue shrinks (decisions, denials).
func (p *approvalPanel) Clamp(n int) { p.cursor = max(0, min(p.cursor, n-1)) }

// Reset returns the panel to its initial state (cursor at 0, collapsed).
func (p *approvalPanel) Reset() {
	p.cursor = 0
	p.Collapse()
}
