package main

// The waiting animation: a pearl grows grain by grain inside an open
// shell, shimmers while a highlight sweeps across the label, then the shell
// snaps shut and it starts over.

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/lucasb-eyer/go-colorful"
	"github.com/muesli/termenv"
)

const (
	frameRate    = time.Second / 16
	growStep     = 260 * time.Millisecond
	shimmerTime  = 900 * time.Millisecond
	shutTime     = 420 * time.Millisecond
	shellColor   = "#C8A27A"
	labelColor   = "#8A857B"
	shimmerColor = "#FFFFFF"
	shutShell    = "(‿‿‿)"
)

var (
	// The pearl's stages as it grows.
	pearlStages = []string{"·", "∘", "○", "◎", "●"}
	// Pearly colors the grown pearl cycles through while it shimmers.
	pearlColors = []string{"#F4EFE6", "#EADCF2", "#DCE9F5", "#D8F0EC", "#F3E6EE"}
	cycle       = time.Duration(len(pearlStages))*growStep + shimmerTime + shutTime
)

// pearlFrames are the same frames at a steady growStep pace, for the plain
// spinner, which draws no color.
func pearlFrames() []string {
	var frames []string
	for _, p := range pearlStages {
		frames = append(frames, "( "+p+" )")
	}
	return append(frames, "( ● )", "( ● )", shutShell, shutShell)
}

type frameMsg struct{}

type anim struct {
	start, now time.Time
	label      string
	color      bool
	shell, dim lipgloss.Style
	pearls     []lipgloss.Style
	// shimmer[d] colors a label letter d letters from the highlight.
	shimmer []lipgloss.Style
}

func newAnim(label string) anim {
	now := time.Now()
	a := anim{start: now, now: now, label: label}
	// Colors only look right in truecolor; elsewhere stay plain.
	if lipgloss.ColorProfile() != termenv.TrueColor {
		return a
	}
	a.color = true
	a.shell = lipgloss.NewStyle().Foreground(lipgloss.Color(shellColor))
	a.dim = lipgloss.NewStyle().Foreground(lipgloss.Color(labelColor))
	for _, c := range pearlColors {
		a.pearls = append(a.pearls, lipgloss.NewStyle().Foreground(lipgloss.Color(c)).Bold(true))
	}
	from, _ := colorful.Hex(shimmerColor)
	to, _ := colorful.Hex(labelColor)
	for i := range 4 {
		a.shimmer = append(a.shimmer, lipgloss.NewStyle().Foreground(lipgloss.Color(from.BlendLuv(to, float64(i)/4).Hex())))
	}
	return a
}

func (a anim) Init() tea.Cmd { return tick() }

func tick() tea.Cmd {
	return tea.Tick(frameRate, func(time.Time) tea.Msg { return frameMsg{} })
}

func (a anim) Update(msg tea.Msg) (anim, tea.Cmd) {
	if _, ok := msg.(frameMsg); ok {
		a.now = time.Now()
		return a, tick()
	}
	return a, nil
}

func (a anim) View() string {
	t := a.now.Sub(a.start) % cycle
	grown := time.Duration(len(pearlStages)) * growStep
	pearl, shut := pearlStages[len(pearlStages)-1], false
	shimmer := -1.0 // how far the highlight has swept, from 0 to 1
	switch {
	case t < grown:
		pearl = pearlStages[t/growStep]
	case t < grown+shimmerTime:
		shimmer = float64(t-grown) / float64(shimmerTime)
	default:
		shut = true
	}

	if !a.color {
		if shut {
			return shutShell + "  " + a.label
		}
		return "( " + pearl + " )  " + a.label
	}

	var b strings.Builder
	if shut {
		b.WriteString(a.shell.Render(shutShell))
	} else {
		style := a.pearls[0]
		if shimmer >= 0 {
			style = a.pearls[int(shimmer*float64(len(a.pearls)))%len(a.pearls)]
		}
		b.WriteString(a.shell.Render("( ") + style.Render(pearl) + a.shell.Render(" )"))
	}
	b.WriteString("  ")
	// The highlight starts before the first letter and ends past the last.
	letters := []rune(a.label)
	reach := len(a.shimmer)
	at := shimmer*float64(len(letters)+2*reach) - float64(reach)
	for i, r := range letters {
		style := a.dim
		if d := int(max(float64(i)-at, at-float64(i))); shimmer >= 0 && d < reach {
			style = a.shimmer[d]
		}
		b.WriteString(style.Render(string(r)))
	}
	return b.String()
}
