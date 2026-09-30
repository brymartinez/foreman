package tui

// minWidth and minHeight are the smallest terminal the dashboard draws in. Below
// them Frame says what size it needs instead of drawing a broken layout.
const minWidth, minHeight = 60, 12

// layout is one frame's row budget. The rows are, top to bottom: a header, topHeight
// rows of side-by-side panes, an optional log, a message, and key hints.
type layout struct {
	topHeight int
	logHeight int
	// leftWidth is the inbox pane's column count; the task pane gets the rest, less
	// the one column between them. The split is uneven because a task row carries a
	// padded identity, an agent, a state, and its badges.
	leftWidth int
}

func layoutFor(width, height int, showLog bool) (layout, bool) {
	if width < minWidth || height < minHeight {
		return layout{}, false
	}
	if !showLog {
		return layout{topHeight: height - 3, leftWidth: width * 9 / 20}, true
	}
	body := height - 4
	// A heading plus taskRowLines body rows let a narrow selected task show its
	// wrapped state, title, and PR badges. At the minimum height, the log still has a
	// heading and line.
	top := max(body*3/4, 1+taskRowLines)
	top = min(top, body-2)
	return layout{topHeight: top, logHeight: body - top, leftWidth: width * 9 / 20}, true
}
