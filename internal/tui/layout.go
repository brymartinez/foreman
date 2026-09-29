package tui

// minWidth and minHeight are the smallest terminal the dashboard draws in. Below
// them Frame says what size it needs instead of drawing a broken layout.
const minWidth, minHeight = 60, 12

// layout is one frame's row budget. The rows are, top to bottom: a header, topHeight
// rows of side-by-side panes, a divider, logHeight rows of log, a message, and the
// key hints. Each pane spends its first row on its own heading.
type layout struct {
	topHeight int
	logHeight int
	// leftWidth is the inbox pane's column count; the task pane gets the rest, less
	// the one column between them. The split is uneven because a task row carries a
	// padded identity, an agent, a state, and its badges.
	leftWidth int
}

func layoutFor(width, height int) (layout, bool) {
	if width < minWidth || height < minHeight {
		return layout{}, false
	}
	body := height - 4
	top := max(body*2/5, 4)
	return layout{topHeight: top, logHeight: body - top, leftWidth: width * 9 / 20}, true
}
