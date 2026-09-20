package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/BT10011/shoal/internal/model"
)

var detailOrder = []model.Field{
	model.FieldIP, model.FieldMAC, model.FieldHostname, model.FieldVendor, model.FieldLatency,
	model.FieldType, model.FieldService, model.FieldPort, model.FieldFlag, model.FieldFirstSeen,
}

// rawLimit caps the hex view of one observation. mDNS announcements can run
// to several kilobytes; the first kilobyte shows the headers and the
// records that matter, and the trailer says how much was left out.
const rawLimit = 1024

// renderDetails lists everything known about the selected device, grouped
// by field and then by source, each value followed by how it was learned,
// the probe, its confidence, age and expiry. With the raw view on, the
// packet or answer behind each value follows as a hex dump.
func (a *app) renderDetails(width int) []string {
	s := a.renderer.Styles
	d, ok := a.current()
	if !ok {
		return []string{s.ItemMuted.Render(fit("select a device to see how each value was learned", width))}
	}

	title := d.Key
	if h, ok := d.ResolvedAt(model.FieldHostname, a.now); ok {
		title = h.Value + "  " + d.Key
	}
	fr := classify(d, a.status)
	lines := []string{s.DetailTitle.Render(ansi.Truncate(displaySafe(title), max(1, width-2), "…"))}
	first, _ := firstSeen(d, a.now)
	last, _, heard := d.LastContact()
	meta := fmt.Sprintf("first seen %s", stamp(a.now, first))
	if heard {
		meta += fmt.Sprintf(" · last heard %s (%s)", stamp(a.now, last), ago(a.now, last))
	}
	for _, l := range wrap(meta, width) {
		lines = append(lines, s.DetailMeta.Render(fit(l, width)))
	}
	freshStyle := s.DetailMeta
	if fr == notAnswering {
		freshStyle = a.alert()
	}
	for i, l := range wrap(fr.explain(d, a.status, a.now), width-2) {
		prefix := "  "
		if i == 0 {
			prefix = fr.mark(s.PlainUI) + " "
		}
		lines = append(lines, freshStyle.Render(fit(prefix+l, width)))
	}
	if remembered(d) {
		for _, l := range wrap("remembered from an earlier visit to this network and not heard on this one: every value below is what it was then", width-2) {
			lines = append(lines, s.DetailMeta.Render(fit("  "+l, width)))
		}
	}
	if len(d.Live(model.FieldIP, a.now)) == 0 {
		for _, l := range wrap("seen at layer 2 only: no probe has learned an address for this device; it may still be probing for one", width-2) {
			lines = append(lines, s.DetailMeta.Render(fit("  "+l, width)))
		}
	}

	label := s.Item.Bold(true)
	muted := s.ItemMuted
	conflict := a.alert()
	hasRaw := false
	for _, f := range detailOrder {
		live := d.Live(f, a.now)
		if len(live) == 0 {
			continue
		}
		lines = append(lines, "")
		seen := map[string]bool{}
		for i, o := range live {
			name := ""
			if i == 0 {
				name = string(f)
			}
			value := o.Value
			if f == model.FieldFirstSeen {
				if t, err := time.Parse(time.RFC3339, o.Value); err == nil {
					value = stamp(a.now, t)
				}
			}
			style := s.Item
			switch {
			case model.Historical(o.Source):
				value += "  (last visit)"
				style = muted
			case !f.MultiValued() && i > 0 && !seen[o.Value]:
				value += "  (disagrees)"
				style = conflict
			case !f.MultiValued() && i > 0:
				value += "  (agrees)"
				style = muted
			}
			seen[o.Value] = true
			// The gap between the label and the value is a styled space: a raw
			// one would sit after the label's reset with no background of its
			// own, letting a transparent terminal show through right before
			// the value.
			lines = append(lines, label.Render(fit(name, 10))+s.Item.Render(" ")+style.Render(fit(value, max(1, width-11))))
			for i, l := range wrap(o.Method, width-4) {
				prefix := "    "
				if i == 0 {
					prefix = "  ← "
					if s.PlainUI {
						prefix = "  < "
					}
				}
				lines = append(lines, muted.Render(fit(prefix+l, width)))
			}
			lines = append(lines, muted.Render(fit(fmt.Sprintf("    %s · conf %.1f · %s · %s", o.Source, o.Confidence, ago(a.now, o.At), ttl(o, a.now)), width)))
			if len(o.Raw) > 0 {
				hasRaw = true
			}
			if !a.showRaw {
				continue
			}
			if len(o.Raw) == 0 {
				lines = append(lines, muted.Render(fit("    (no raw packet kept for this value)", width)))
				continue
			}
			lines = append(lines, muted.Render(fit(fmt.Sprintf("    raw: %d bytes", len(o.Raw)), width)))
			for _, l := range hexDump(o.Raw, width-4) {
				lines = append(lines, s.Item.Render(fit("    "+l, width)))
			}
		}
	}
	if hasRaw && !a.showRaw {
		lines = append(lines, "", muted.Render(fit("x shows the raw packet behind each value", width)))
	}
	return lines
}

// hexDump formats b as offset, hex bytes and printable ASCII, choosing how
// many bytes per line fit the width.
func hexDump(b []byte, width int) []string {
	per := 16
	switch {
	case width < 39:
		per = 4
	case width < 72:
		per = 8
	}
	shown := b
	if len(shown) > rawLimit {
		shown = shown[:rawLimit]
	}
	hexWidth := per*3 - 1
	if per == 16 {
		hexWidth++ // the extra gap between the two halves
	}
	var lines []string
	for off := 0; off < len(shown); off += per {
		chunk := shown[off:min(off+per, len(shown))]
		var hexs, text strings.Builder
		for i, c := range chunk {
			if i > 0 {
				hexs.WriteByte(' ')
				if per == 16 && i == 8 {
					hexs.WriteByte(' ')
				}
			}
			fmt.Fprintf(&hexs, "%02x", c)
			if c >= 0x20 && c < 0x7f {
				text.WriteByte(c)
			} else {
				text.WriteByte('.')
			}
		}
		lines = append(lines, fmt.Sprintf("%04x  %-*s  %s", off, hexWidth, hexs.String(), text.String()))
	}
	if len(b) > rawLimit {
		lines = append(lines, fmt.Sprintf("… %d more bytes not shown", len(b)-rawLimit))
	}
	return lines
}
