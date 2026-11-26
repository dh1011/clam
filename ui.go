package main

// Drawing answers: markdown streamed through glamour behind a waiting
// animation, the way charmbracelet/mods does, or plain text when the
// output is not a terminal.

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

const maxWrap = 100

// output shows one stretch of answer text.
type output interface {
	write(text string)
	// close finishes the output and reports whether the user pressed
	// ctrl+c while it was showing.
	close() bool
}

type (
	chunkMsg string
	eofMsg   struct{}
)

type streamModel struct {
	glam        *glamour.TermRenderer
	anim        anim
	raw         []byte
	rendered    string
	done        bool
	interrupted bool
}

func (m streamModel) Init() tea.Cmd { return m.anim.Init() }

func (m streamModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case chunkMsg:
		m.raw = append(m.raw, msg...)
		m.rendered = m.render()
		return m, nil
	case eofMsg:
		m.done = true
		return m, tea.Quit
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.interrupted = true
			return m, tea.Quit
		}
		return m, nil
	}
	if len(m.raw) > 0 {
		return m, nil // the animation stops once the answer starts
	}
	var cmd tea.Cmd
	m.anim, cmd = m.anim.Update(msg)
	return m, cmd
}

// render re-renders the whole answer so far, so markdown that was
// incomplete in an earlier chunk (an open code fence, say) comes out right.
func (m streamModel) render() string {
	raw := m.raw
	// Don't render a multi-byte character that was split across chunks.
	for len(raw) > 0 && !utf8.Valid(raw) {
		raw = raw[:len(raw)-1]
	}
	out, err := m.glam.Render(string(raw))
	if err != nil {
		out = string(raw)
	}
	out = strings.TrimRightFunc(out, unicode.IsSpace)
	return strings.ReplaceAll(out, "\t", "    ") + "\n"
}

// View shows the animation, then the live render. Bubble Tea keeps only the
// bottom of a view taller than the terminal; the full answer is printed once
// the program exits, so it all lands in the scrollback.
func (m streamModel) View() string {
	switch {
	case m.done || m.interrupted:
		return ""
	case len(m.raw) == 0:
		return m.anim.View() + "\n"
	default:
		return m.rendered
	}
}

func wrapWidth() int {
	width, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || width <= 0 {
		return 80
	}
	return min(width-4, maxWrap)
}

// renderer asks the terminal for its background once, through lipgloss,
// instead of letting glamour's auto style query it for every output.
var renderer = sync.OnceValues(func() (*glamour.TermRenderer, error) {
	style := os.Getenv("GLAMOUR_STYLE")
	if style == "" {
		style = "light"
		if lipgloss.HasDarkBackground() {
			style = "dark"
		}
	}
	return glamour.NewTermRenderer(glamour.WithStylePath(style), glamour.WithWordWrap(wrapWidth()))
})

// uiOutput renders the answer as it streams in. Keys reach it in raw mode,
// so ctrl+c arrives as a key press rather than a signal; onInterrupt is
// called right away so the caller can stop waiting on the model.
type uiOutput struct {
	program *tea.Program
	final   chan streamModel
}

func newUIOutput(onInterrupt func()) (output, error) {
	glam, err := renderer()
	if err != nil {
		return nil, err
	}
	o := &uiOutput{
		program: tea.NewProgram(streamModel{glam: glam, anim: newAnim("thinking")}),
		final:   make(chan streamModel, 1),
	}
	go func() {
		model, err := o.program.Run()
		final, _ := model.(streamModel)
		if err != nil {
			final.raw = nil
		}
		if final.interrupted {
			onInterrupt()
		}
		o.final <- final
	}()
	return o, nil
}

func (o *uiOutput) write(text string) { o.program.Send(chunkMsg(text)) }

func (o *uiOutput) close() bool {
	o.program.Send(eofMsg{})
	final := <-o.final
	if len(final.raw) > 0 {
		fmt.Print(final.rendered)
	}
	return final.interrupted
}

// plainOutput prints the answer as plain text, behind a simple version of
// the pearl animation on stderr.
type plainOutput struct {
	stop    chan struct{}
	stopped sync.WaitGroup
	started bool
}

func newPlainOutput() output {
	o := &plainOutput{}
	if !term.IsTerminal(int(os.Stderr.Fd())) {
		return o
	}
	o.stop = make(chan struct{})
	o.stopped.Add(1)
	go func() {
		defer o.stopped.Done()
		frames := pearlFrames()
		ticker := time.NewTicker(growStep)
		defer ticker.Stop()
		for i := 0; ; i++ {
			fmt.Fprintf(os.Stderr, "\r\x1b[2m%s  thinking\x1b[0m", frames[i%len(frames)])
			select {
			case <-o.stop:
				fmt.Fprint(os.Stderr, "\r\x1b[K")
				return
			case <-ticker.C:
			}
		}
	}()
	return o
}

func (o *plainOutput) stopSpinner() {
	if o.stop != nil {
		close(o.stop)
		o.stopped.Wait()
		o.stop = nil
	}
}

func (o *plainOutput) write(text string) {
	if !o.started {
		o.stopSpinner()
		o.started = true
	}
	fmt.Print(text)
}

func (o *plainOutput) close() bool {
	o.stopSpinner()
	if o.started {
		fmt.Println()
	}
	return false
}
