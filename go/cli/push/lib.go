package push

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	pb "premai.io/Ayup/go/internal/grpc/srv"
	"premai.io/Ayup/go/internal/rpc"
	"premai.io/Ayup/go/internal/trace"

	tr "go.opentelemetry.io/otel/trace"
)

type Pusher struct {
	Tracer tr.Tracer

	Host       string
	P2pPrivKey string
	Client     pb.SrvClient

	SrcDir string
}

type LogView struct {
	name string

	done       bool
	cancelChan chan struct{}
	hist       *strings.Builder

	spinner spinner.Model

	braceStyle  lipgloss.Style
	nameStyle   lipgloss.Style
	sourceStyle lipgloss.Style
}

type DoneMsg struct{}
type LogMsg struct {
	source string
	body   string
}

func NewLogView(name string, cancelChan chan struct{}) LogView {
	var hist strings.Builder
	s := spinner.New()
	s.Spinner = spinner.Points
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))

	return LogView{
		name:       name,
		cancelChan: cancelChan,
		hist:       &hist,
		spinner:    s,

		braceStyle:  lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("008")),
		nameStyle:   lipgloss.NewStyle().Foreground(lipgloss.Color("098")),
		sourceStyle: lipgloss.NewStyle().Foreground(lipgloss.Color("060")),
	}
}

func (s LogView) Init() tea.Cmd {
	return s.spinner.Tick
}

func (s LogView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			s.cancelChan <- struct{}{}
			return s, nil
		}
	case DoneMsg:
		s.done = true
		return s, tea.Quit
	case LogMsg:
		if s.hist.Len() > 0 {
			s.hist.WriteString("\n")
		}
		s.hist.WriteString(fmt.Sprintf(
			"%s%s%s%s%s %s",
			s.braceStyle.Render("["),
			s.nameStyle.Render(s.name),
			s.braceStyle.Render("/"),
			s.sourceStyle.Render(msg.source),
			s.braceStyle.Render("]"),
			msg.body,
		))
	case spinner.TickMsg:
		var cmd tea.Cmd
		s.spinner, cmd = s.spinner.Update(msg)
		return s, cmd
	}

	return s, nil
}

func (s LogView) View() string {
	if !s.done {
		if s.hist.Len() > 0 {
			return fmt.Sprintf("%s %s\n", s.hist.String(), s.spinner.View())
		} else {
			return fmt.Sprintf(
				"%s%s%s %s",
				s.braceStyle.Render("["),
				s.nameStyle.Render(s.name),
				s.braceStyle.Render("]"),
				s.spinner.View(),
			)
		}
	} else {
		return fmt.Sprintf("%s\n\n", s.hist.String())
	}
}

func (s *Pusher) Run(ctx context.Context) error {
	ctx = trace.SetSpanKind(ctx, tr.SpanKindClient)
	ctx, span := trace.Span(ctx, "push")
	defer span.End()

	privKey, err := rpc.EnsurePrivKey(ctx, "AYUP_CLIENT_P2P_PRIV_KEY", s.P2pPrivKey)
	if err != nil {
		return err
	}

	client, err := rpc.Client(ctx, s.Host, privKey)
	if err != nil {
		return err
	}
	s.Client = client

	if err := s.Sync(ctx); err != nil {
		return err
	}

	_, err = s.Analysis(ctx)
	if err != nil {
		return err
	}

	return s.RunDocker(ctx)
}
