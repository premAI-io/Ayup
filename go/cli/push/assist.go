package push

import (
	"bytes"
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

type AssistView struct {
	ctx       context.Context
	span      tr.Span
	stream    pb.Srv_AssistClient
	forwarder *Forwarder

	choice       *huh.Form
	spinner      spinner.Model
	hist         *strings.Builder
	histContLine bool
	histPrevSrc  string

	done bool
	err  error

	braceStyle  lipgloss.Style
	nameStyle   lipgloss.Style
	sourceStyle lipgloss.Style
}

type choiceMsg *pb.ChoiceBool

func NewAssistView(ctx context.Context, stream pb.Srv_AssistClient, fwd *Forwarder) AssistView {
	var hist strings.Builder
	s := spinner.New()
	s.Spinner = spinner.Points
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))

	return AssistView{
		ctx:       ctx,
		span:      tr.SpanFromContext(ctx),
		hist:      &hist,
		stream:    stream,
		forwarder: fwd,

		spinner: s,

		braceStyle:  lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("008")),
		nameStyle:   lipgloss.NewStyle().Foreground(lipgloss.Color("098")),
		sourceStyle: lipgloss.NewStyle().Foreground(lipgloss.Color("060")),
	}
}

func (s AssistView) recvMsgCmd() tea.Cmd {
	return func() tea.Msg {
		res, err := s.stream.Recv()

		if err != nil {
			if err == io.EOF {
				return func() DoneMsg { return DoneMsg{} }
			}
			return terror.Errorf(s.ctx, "stream recv: %w", err)
		}

		if res.Variant == nil {
			trace.Event(s.ctx, "recv nil done")
			return DoneMsg{}
		}

		switch v := res.Variant.(type) {
		case *pb.ActReply_Error:
			return terror.Errorf(s.ctx, "%s", v.Error)
		case *pb.ActReply_Log:
			return LogMsg{
				source: res.GetSource(),
				body:   v.Log,
			}
		case *pb.ActReply_Choice:
			choice := v.Choice.GetBool()
			if choice != nil {
				return choiceMsg(choice)
			}
		case *pb.ActReply_Expose:
			if err := s.forwarder.startPortForwarder(s.ctx, v.Expose.Port); err != nil {
				return LogMsg{
					source: "proxy",
					body:   fmt.Sprintf("Couldn't forward port: %d: %s", v.Expose.Port, err.Error()),
				}
			}
			return LogMsg{
				source: "proxy",
				body:   fmt.Sprintf("Forwarding port: %d", v.Expose.Port),
			}
		}

		return terror.Errorf(s.ctx, "Can't handle remote response: %v", res)
	}
}

func (s AssistView) sendCmd(req *pb.ActReq) tea.Cmd {
	return func() tea.Msg {
		s.span.AddEvent("sending req")
		if err := s.stream.Send(req); err != nil {
			terror.Ackf(s.ctx, "stream sendMsg: %w", err)
			return DoneMsg{}
		}

		return nil
	}
}

func (s AssistView) Init() tea.Cmd {
	return tea.Batch(s.recvMsgCmd(), s.spinner.Tick)
}

const formKeyBool = "bool"

func (s AssistView) fmtLogHeader(source string) string {
	return fmt.Sprintf(
		"%s%s%s%s%s ",
		s.braceStyle.Render("["),
		s.nameStyle.Render("assist"),
		s.braceStyle.Render("/"),
		s.sourceStyle.Render(source),
		s.braceStyle.Render("]"),
	)
}

func (s AssistView) writeLogHeader(source string) {
	s.hist.WriteString(s.fmtLogHeader(source))
}

func (s AssistView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		bs := []byte(msg.body)

		if s.histPrevSrc != msg.source && s.histContLine {
			s.histContLine = false
			s.histPrevSrc = msg.source
			s.hist.WriteByte('\n')
		}

		if len(bs) < 1 {
			trace.Event(s.ctx, "received empty log message", attr.String("source", msg.source))
			return s, s.recvMsgCmd()
		} else {
			trace.Event(s.ctx, "received log message", attr.String("source", msg.source), attr.String("body", msg.body))
		}

		for {
			i := bytes.IndexByte(bs, '\n')
			if i == -1 {
				break
			}

			line := bs[:i+1]
			bs = bs[i+1:]

			if s.histContLine {
				s.hist.Write(line)
				s.histContLine = false
				continue
			}

			s.writeLogHeader(msg.source)
			s.hist.Write(line)

			if len(bs) < 1 {
				return s, s.recvMsgCmd()
			}
		}

		if !s.histContLine {
			s.writeLogHeader(msg.source)
		}
		s.hist.Write(bs)
		s.histContLine = true

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

func (s AssistView) View() string {
	if s.done {
		return fmt.Sprintf("%s\n", s.hist.String())
	}

	if s.choice != nil {
		return fmt.Sprintf(
			"%s\n%s",
			s.hist.String(),
			s.choice.View(),
		)
	}

	if s.histContLine {
		return fmt.Sprintf("%s %s\n", s.hist.String(), s.spinner.View())
	} else if s.hist.Len() < 1 {
		return fmt.Sprintf(
			"%s %s",
			s.fmtLogHeader("..."),
			s.spinner.View(),
		)
	} else {
		return fmt.Sprintf(
			"%s%s %s",
			s.hist.String(),
			s.fmtLogHeader("..."),
			s.spinner.View(),
		)
	}
}

func (s *Pusher) Assist(pctx context.Context, fwd *Forwarder) (err error) {
	ctx, span := trace.Span(pctx, "assist")
	defer span.End()

	stream, err := s.Client.Assist(ctx)
	if err != nil {
		return terror.Errorf(ctx, "client assist: %w", err)
	}
	defer func() {
		err2 := stream.CloseSend()
		if err == nil {
			err = err2
		}
	}()

	err = stream.Send(&pb.ActReq{})
	if err != nil {
		return err
	}

	view := NewAssistView(ctx, stream, fwd)
	prog := tea.NewProgram(view, tea.WithContext(ctx))
	model, err := prog.Run()
	if err != nil {
		return err
	}

	view = model.(AssistView)

	if view.err != nil {
		return view.err
	}

	msg, err := stream.Recv()
	if msg != nil {
		trace.Event(ctx, "stream rcv", attr.String("msg", msg.String()))
	}
	if err != io.EOF {
		return terror.Errorf(ctx, "stream recv should end: %w", err)
	}

	return nil
}
