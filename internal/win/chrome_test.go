//go:build windows

package win

import "testing"

// The title bar is the app's own, and the window manager is told about it
// through the hit test. Getting it wrong means the window either cannot be
// dragged or cannot be resized.

func TestTheTitleBarStripIsACaption(t *testing.T) {
	const width = 1000
	const height = 700

	// Anywhere in the strip that is not a button drags the window.
	for _, x := range []int{10, 400, 800} {
		if got := TitleBarHitTest(width, height, x, 5); got != htCaption {
			t.Fatalf("x=%d: want a caption, got %d", x, got)
		}
	}
	// Below the strip it is ordinary client area.
	if got := TitleBarHitTest(width, height, 400, TitleBarHeight); got != htClient {
		t.Fatalf("below the strip: want the client area, got %d", got)
	}
	if got := TitleBarHitTest(width, height, 400, TitleBarHeight+10); got != htClient {
		t.Fatalf("below the strip: want the client area, got %d", got)
	}
	// Above the window is not the title bar either.
	if got := TitleBarHitTest(width, height, 400, -1); got != htClient {
		t.Fatalf("above the window: want the client area, got %d", got)
	}
}

// The system frame is gone, so the edges have to report the resize codes
// or the window cannot be resized at all.
func TestTheEdgesResizeTheWindow(t *testing.T) {
	const width = 1000
	const height = 700

	cases := []struct {
		name string
		x, y int
		want int
	}{
		{"left edge", 2, 300, htLeft},
		{"right edge", width - 2, 300, htRight},
		{"bottom edge", 400, height - 2, htBottom},
		{"bottom left corner", 2, height - 2, htBottomLeft},
		{"bottom right corner", width - 2, height - 2, htBottomRight},
		{"middle of the window", 400, 300, htClient},
	}
	for _, c := range cases {
		if got := TitleBarHitTest(width, height, c.x, c.y); got != c.want {
			t.Fatalf("%s: want %d, got %d", c.name, c.want, got)
		}
	}
}

func TestTheWindowButtonsAreHandledByTheInterface(t *testing.T) {
	const width = 1000
	const height = 700
	left, _ := TitleBarButtonBox(width)

	// The buttons are reported as ordinary client area, so the interface
	// draws them and their hover state works.
	if got := TitleBarHitTest(width, height, left+10, 5); got != htClient {
		t.Fatalf("over a button: want the client area, got %d", got)
	}
	// But the hit test still knows which button is under the pointer.
	for i := 0; i < TitleBarButtonCount; i++ {
		x := left + i*(TitleBarButtonWidth+TitleBarButtonGap) + 4
		if got := TitleBarButtonHitTest(width, x, 5); got != i {
			t.Fatalf("x=%d: want button %d, got %d", x, i, got)
		}
	}
	// Anywhere else is not a button.
	if got := TitleBarButtonHitTest(width, 100, 5); got != -1 {
		t.Fatalf("want no button, got %d", got)
	}
	// Nor is a point below the strip.
	if got := TitleBarButtonHitTest(width, left+10, TitleBarHeight+5); got != -1 {
		t.Fatalf("below the strip: want no button, got %d", got)
	}
}

// The edges the page reports have to reach the matching system command. A
// wrong direction resizes the window from the opposite side, which looks
// like the window jumping while one corner is dragged.
func TestTheResizeEdgesUseTheMatchingSystemCommands(t *testing.T) {
	cases := []struct {
		edge string
		hit  int
		wmsz uintptr
	}{
		{"left", htLeft, 1},
		{"right", htRight, 2},
		{"top", htTop, 3},
		{"top-left", htTopLeft, 4},
		{"top-right", htTopRight, 5},
		{"bottom", htBottom, 6},
		{"bottom-left", htBottomLeft, 7},
		{"bottom-right", htBottomRight, 8},
	}
	for _, c := range cases {
		hit, ok := resizeHitFor(c.edge)
		if !ok || hit != c.hit {
			t.Fatalf("%s: want hit %d, got %d (ok %v)", c.edge, c.hit, hit, ok)
		}
		command, ok := systemCommandFor(hit)
		if !ok || command != scSize|c.wmsz {
			t.Fatalf("%s: want SC_SIZE|%d, got %#x (ok %v)", c.edge, c.wmsz, command, ok)
		}
	}
	if _, ok := resizeHitFor("middle"); ok {
		t.Fatal("the middle of the window is not a resize edge")
	}
}

// The interface and the native side have to agree on where the buttons
// are, or the window would be dragged when a button was pressed.
func TestTheInterfaceAndTheNativeSideAgreeOnTheButtons(t *testing.T) {
	const width = 1000
	left, _ := TitleBarButtonBox(width)

	// The group sits a short way from the edge, so it never overlaps the
	// resize frame.
	if left != width-TitleBarButtonEdge-TitleBarButtonGroupWidth {
		t.Fatalf("the group should start at %d, got %d", width-TitleBarButtonEdge-TitleBarButtonGroupWidth, left)
	}
	// The first button starts exactly where the title bar says it does.
	if got := TitleBarButtonHitTest(width, left, 5); got != 0 {
		t.Fatalf("the first button should start at %d, got %d", left, got)
	}
	// And the last one ends before the group's own padding.
	last := left + (TitleBarButtonCount-1)*(TitleBarButtonWidth+TitleBarButtonGap)
	if got := TitleBarButtonHitTest(width, last+TitleBarButtonWidth-1, 5); got != TitleBarButtonCount-1 {
		t.Fatalf("the last button should end at %d, got %d", last+TitleBarButtonWidth-1, got)
	}
	// One pixel further on is off the buttons.
	if got := TitleBarButtonHitTest(width, last+TitleBarButtonWidth, 5); got != -1 {
		t.Fatalf("past the last button: want no button, got %d", got)
	}
}

// The window buttons never move: the bar keeps the order it has in
// English, in every language, so the native side has one case and not
// two.
func TestTheWindowButtonsStayOnTheTrailingEdge(t *testing.T) {
	const width = 1000
	const height = 700

	left, right := TitleBarButtonBox(width)
	if left != width-TitleBarButtonEdge-TitleBarButtonGroupWidth {
		t.Fatalf("the group should start at %d, got %d", width-TitleBarButtonEdge-TitleBarButtonGroupWidth, left)
	}
	if right != width-TitleBarButtonEdge {
		t.Fatalf("the group should end %d pixels from the edge, got %d", TitleBarButtonEdge, right)
	}
	if got := TitleBarHitTest(width, height, left+10, 5); got != htClient {
		t.Fatalf("over a button: want the client area, got %d", got)
	}
	// The rest of the bar is a caption, so the window still drags from it.
	if got := TitleBarHitTest(width, height, 40, 5); got != htCaption {
		t.Fatalf("the bar should be a caption, got %d", got)
	}
}

// On a display that is not at 100 % the system sends physical pixels
// while the title bar is laid out in device-independent ones. The two have
// to be brought together, or the drag zone is short and the buttons sit
// where they are not drawn.
func TestTheHitTestFollowsTheDisplayScale(t *testing.T) {
	const scale = 1.5
	const width = 1500 // 1000 logical pixels
	const height = 1050

	// The bar is 40 logical pixels tall, so 60 physical ones.
	if got := titleBarHitTest(width, height, 700, 55, scale); got != htCaption {
		t.Fatalf("inside the bar: want a caption, got %d", got)
	}
	if got := titleBarHitTest(width, height, 700, 70, scale); got != htClient {
		t.Fatalf("below the bar: want the client area, got %d", got)
	}

	// The button group is 108 logical pixels wide, so 162 physical ones,
	// and it ends 9 physical pixels before the edge.
	left, right := titleBarButtonBox(width, scale)
	if right != width-9 {
		t.Fatalf("the group should end 9 pixels from the edge, got %d", right)
	}
	if got := titleBarHitTest(width, height, right-5, 20, scale); got != htClient {
		t.Fatalf("over a button: want the client area, got %d", got)
	}
	// Just left of the group is the caption again.
	if got := titleBarHitTest(width, height, left-5, 20, scale); got != htCaption {
		t.Fatalf("beside the buttons: want a caption, got %d", got)
	}
	// And the resize frame is 9 physical pixels wide.
	if got := titleBarHitTest(width, height, width-2, 20, scale); got != htRight {
		t.Fatalf("the right edge: want the resize code, got %d", got)
	}
}
