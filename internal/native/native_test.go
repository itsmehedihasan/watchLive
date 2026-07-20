//go:build windows

package native

import "testing"

// TestTileRects checks the auto-tile layout: rects stay inside the area, cover
// it exactly (no gaps/overlap by construction), and match the expected count.
func TestTileRects(t *testing.T) {
	area := rect{left: 0, top: 0, right: 1920, bottom: 1080}
	for n := 1; n <= 4; n++ {
		rs := tileRects(area, n)
		if len(rs) != n {
			t.Fatalf("n=%d: got %d rects, want %d", n, len(rs), n)
		}
		for i, r := range rs {
			if r.left < area.left || r.top < area.top || r.right > area.right || r.bottom > area.bottom {
				t.Errorf("n=%d rect %d %+v escapes area %+v", n, i, r, area)
			}
			if r.right <= r.left || r.bottom <= r.top {
				t.Errorf("n=%d rect %d %+v is degenerate", n, i, r)
			}
		}
	}
	// n<=0 yields nothing; n>4 clamps to a 2×2 (4 rects).
	if got := tileRects(area, 0); got != nil {
		t.Errorf("n=0: got %v, want nil", got)
	}
	if got := tileRects(area, 9); len(got) != 4 {
		t.Errorf("n=9: got %d rects, want 4 (clamped)", len(got))
	}
}

// TestTileRectsTwoUp verifies the 2-up split covers the full width with no gap.
func TestTileRectsTwoUp(t *testing.T) {
	rs := tileRects(rect{left: 100, top: 50, right: 1100, bottom: 850}, 2)
	if rs[0].left != 100 || rs[1].right != 1100 {
		t.Errorf("2-up does not span the area: %+v", rs)
	}
	if rs[0].right != rs[1].left {
		t.Errorf("2-up has a gap/overlap at the seam: %d vs %d", rs[0].right, rs[1].left)
	}
	if rs[0].top != 50 || rs[0].bottom != 850 {
		t.Errorf("2-up should be full height: %+v", rs[0])
	}
}

func TestEncodeCommand(t *testing.T) {
	got, err := encodeCommand("loadfile", "http://127.0.0.1:37641/api/proxy?url=x")
	if err != nil {
		t.Fatal(err)
	}
	want := `{"command":["loadfile","http://127.0.0.1:37641/api/proxy?url=x"]}` + "\n"
	if string(got) != want {
		t.Errorf("loadfile encoding:\n got %q\nwant %q", got, want)
	}

	got, err = encodeCommand("set_property", "mute", true)
	if err != nil {
		t.Fatal(err)
	}
	want = `{"command":["set_property","mute",true]}` + "\n"
	if string(got) != want {
		t.Errorf("set_property encoding:\n got %q\nwant %q", got, want)
	}
}
