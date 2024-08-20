package push

import (
	"context"
	"fmt"
	"io"
	"runtime/pprof"
	"strings"

	// "github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"premai.io/Ayup/go/internal/terror"
	"premai.io/Ayup/go/internal/trace"

	attr "go.opentelemetry.io/otel/attribute"
	tr "go.opentelemetry.io/otel/trace"

	pb "premai.io/Ayup/go/internal/grpc/srv"
)

type AnalysisView struct {
	ctx    context.Context
	span   tr.Span
	hist   *strings.Builder
	stream pb.Srv_AnalysisClient

	choice  *huh.Form
	spinner spinner.Model

	done   bool
	result *pb.AnalysisResult
	err    error

	braceStyle  lipgloss.Style
	nameStyle   lipgloss.Style
	sourceStyle lipgloss.Style
}

type choiceMsg *pb.ChoiceBool
type resultMsg *pb.AnalysisResult

func NewAnalysisView(ctx context.Context, stream pb.Srv_AnalysisClient) AnalysisView {
	var hist strings.Builder
	s := spinner.New()
	s.Spinner = spinner.Points
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))

	return AnalysisView{
		ctx:     ctx,
		span:    tr.SpanFromContext(ctx),
		hist:    &hist,
		stream:  stream,
		spinner: s,

		braceStyle:  lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("008")),
		nameStyle:   lipgloss.NewStyle().Foreground(lipgloss.Color("098")),
		sourceStyle: lipgloss.NewStyle().Foreground(lipgloss.Color("060")),
	}
}

func (s AnalysisView) recvMsgCmd() tea.Cmd {
	return func() tea.Msg {
		s.span.AddEvent("waiting for recv")
		res, err := s.stream.Recv()
		s.span.AddEvent("got recv")
		if err != nil {
			if err == io.EOF {
				return func() DoneMsg { return DoneMsg{} }
			}
			return terror.Errorf(s.ctx, "stream recv: %w", err)
		}

		if res.GetError() != nil {
			return terror.Errorf(s.ctx, "%s", res.GetError().Error)
		}

		log := res.GetLog()
		if log != "" {
			return LogMsg{
				source: res.GetSource(),
				body:   log,
			}
		}

		choice := res.GetChoice().GetBool()
		if choice != nil {
			return choiceMsg(choice)
		}

		result := res.GetAnalysisResult()
		if result != nil {
			return resultMsg(result)
		}

		if res.Variant == nil {
			// TODO: sent by proc watcher for success, not needed for analysis anymore
			return DoneMsg{}
		} else {
			return terror.Errorf(s.ctx, "Can't handle remote response: %v", res)
		}
	}
}

func (s AnalysisView) sendCmd(req *pb.ActReq) tea.Cmd {
	return func() tea.Msg {
		s.span.AddEvent("sending req")
		if err := s.stream.Send(req); err != nil {
			terror.Ackf(s.ctx, "stream sendMsg: %w", err)
			return tea.Quit
		}

		return nil
	}
}

func (s AnalysisView) Init() tea.Cmd {
	return tea.Batch(s.recvMsgCmd(), s.spinner.Tick)
}

const formKeyBool = "bool"

func (s AnalysisView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		trace.Event(s.ctx, "key press", attr.String("key", msg.String()))

		switch msg.String() {
		case "ctrl+c":
			return s, s.sendCmd(&pb.ActReq{
				Cancel: true,
			})
		}
	case error:
		s.err = msg
		return s, tea.Quit
	case LogMsg:
		if s.hist.Len() > 0 {
			s.hist.WriteString("\n")
		}
		s.hist.WriteString(fmt.Sprintf(
			"%s%s%s%s%s %s",
			s.braceStyle.Render("["),
			s.nameStyle.Render("analysis"),
			s.braceStyle.Render("/"),
			s.sourceStyle.Render(msg.source),
			s.braceStyle.Render("]"),
			msg.body,
		))
		return s, s.recvMsgCmd()
	case choiceMsg:
		var f *huh.Form

		pprof.Do(s.ctx, pprof.Labels("hotspot", "create form"), func(ctx context.Context) {
			trace.Event(s.ctx, "before create choice field")

			v := msg.Value
			c := huh.NewConfirm().
				Key(formKeyBool).
				Title(msg.Title).
				Description(msg.Description).
				Affirmative(msg.Affirmative).
				Negative(msg.Negative).
				Value(&v)
			s.span.AddEvent("before create choice group")
			g := huh.NewGroup(c)
			s.span.AddEvent("before create choice form")
			f = huh.NewForm(g)
			s.span.AddEvent("create choice form")
			s.choice = f
		})

		return s, tea.Batch(f.Init(), s.recvMsgCmd())
	case DoneMsg:
		if err := s.stream.CloseSend(); err != nil {
			terror.Ackf(s.ctx, "close send: %w", err)
		}

		s.done = true

		return s, tea.Quit
	case spinner.TickMsg:
		var cmd tea.Cmd
		s.spinner, cmd = s.spinner.Update(msg)

		return s, cmd
	}

	if s.choice != nil {
		form, ccmd := s.choice.Update(msg)

		if f, ok := form.(*huh.Form); ok {
			switch f.State {
			case huh.StateNormal:
				s.choice = f
				return s, ccmd
			case huh.StateAborted:
				s.choice = nil
				return s, s.sendCmd(&pb.ActReq{Cancel: true})
			case huh.StateCompleted:
				c := s.choice.GetBool(formKeyBool)
				s.choice = nil
				return s, s.sendCmd(&pb.ActReq{
					Choice: &pb.Chosen{
						Variant: &pb.Chosen_Bool{
							Bool: &pb.ChosenBool{
								Value: c,
							},
						},
					},
				})
			}
		}
	}

	return s, nil
}

func (s AnalysisView) View() string {
	if s.done {
		return fmt.Sprintf("%s\n\n", s.hist.String())
	}

	if s.choice != nil {
		return fmt.Sprintf(
			"%s\n%s",
			s.hist.String(),
			s.choice.View())
	}

	if s.hist.Len() > 0 {
		return fmt.Sprintf("%s %s\n", s.hist.String(), s.spinner.View())
	} else {
		return fmt.Sprintf(
			"%s%s%s %s",
			s.braceStyle.Render("["),
			s.nameStyle.Render("analysis"),
			s.braceStyle.Render("]"),
			s.spinner.View(),
		)
	}
}

func (s *Pusher) Analysis(pctx context.Context) (result *pb.AnalysisResult, err error) {
	ctx, span := trace.Span(pctx, "analysis")
	defer span.End()

	stream, err := s.Client.Analysis(ctx)
	if err != nil {
		return nil, terror.Errorf(ctx, "client analysis: %w", err)
	}
	defer func() {
		err2 := stream.CloseSend()
		if err == nil {
			err = err2
		}
	}()

	err = stream.Send(&pb.ActReq{})
	if err != nil {
		return nil, err
	}

	view := NewAnalysisView(ctx, stream)
	prog := tea.NewProgram(view, tea.WithContext(ctx))
	model, err := prog.Run()

	msg, err := stream.Recv()
	if msg != nil {
		trace.Event(ctx, "stream rcv", attr.String("msg", msg.String()))
	}
	if err != io.EOF {
		return nil, terror.Errorf(ctx, "stream recv should end: %w", err)
	} else {
		err = nil
	}

	view = model.(AnalysisView)

	if view.err != nil {
		err = view.err
	}

	return view.result, err
}
