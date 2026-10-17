package ui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/rivo/uniseg"
)

// LogoWidth is the rendered width of Logo, in terminal columns.
const LogoWidth = 37

// logoWordmark is the letter-spaced "INVOKR" wordmark the diagonal frame in
// Logo pads out to LogoWidth.
const logoWordmark = `▄▄▄▄▄                                
 ███  ████▄ ██ ██ ▄███▄ ██ ▄█▀ ████▄ 
 ███  ██ ██ ██▄██ ██ ██ ████   ██ ▀▀ 
▄███▄ ██ ██  ▀█▀  ▀███▀ ██ ▀█▄ ██    
`

// Logo renders the Invokr wordmark block, LogoWidth columns wide, blended from the theme's primary color (Brand) to its
// secondary color (Blue) across each character.
//
// This is a spike: charmbracelet/crush uses the same technique for its
// title art — split the line into grapheme clusters so multi-byte runes
// never split mid-character, ask lipgloss for an evenly-spaced color ramp
// between the two endpoints, then paint one cluster per ramp step.
func (t Theme) Logo() string { return BlendText(logoWordmark, t.Palette.Brand, t.Palette.Blue) }

// BlendText renders s with a horizontal gradient foreground running from
// `from` to `to`, one color per grapheme cluster.
func BlendText(s string, from, to color.Color) string {
	if s == "" {
		return ""
	}
	var clusters []string
	gr := uniseg.NewGraphemes(s)
	for gr.Next() {
		clusters = append(clusters, string(gr.Runes()))
	}
	ramp := lipgloss.Blend1D(len(clusters), from, to)
	var b strings.Builder
	for i, c := range clusters {
		b.WriteString(lipgloss.NewStyle().Foreground(ramp[i]).Bold(true).Render(c))
	}
	return b.String()
}
