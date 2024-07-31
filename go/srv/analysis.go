package srv

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"

	tr "go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"

	pb "premai.io/Ayup/go/internal/grpc/srv"
	"premai.io/Ayup/go/internal/trace"
)

type ActServer interface {
	Send(*pb.ActReply) error
	Recv() (*pb.ActReq, error)
	grpc.ServerStream
}

func (s *Srv) useDockerfile(ctx context.Context, stream pb.Srv_AnalysisServer) (bool, error) {
	internalError := mkInternalError(ctx, stream)

	_, err := os.Stat(filepath.Join(s.SrcDir, "Dockerfile"))
	if err != nil {
		if !os.IsNotExist(err) {
			return false, internalError("stat Dockerfile: %w", err)
		}

		return false, nil
	}

	if err := stream.Send(&pb.ActReply{
		Source: "ayup",
		Variant: &pb.ActReply_Log{
			Log: "Found Dockerfile, will use it",
		},
	}); err != nil {
		return false, internalError("stream send: %w", err)
	}

	s.push.analysis = &pb.AnalysisResult{
		UseDockerfile: true,
	}

	if err := stream.Send(&pb.ActReply{
		Source: "ayup",
		Variant: &pb.ActReply_AnalysisResult{
			AnalysisResult: s.push.analysis,
		},
	}); err != nil {
		return false, internalError("stream send: %w", err)
	}

	return true, nil
}

func (s *Srv) Analysis(stream pb.Srv_AnalysisServer) error {
	ctx := stream.Context()
	span := tr.SpanFromContext(ctx)
	ctx = trace.SetSpanKind(ctx, tr.SpanKindServer)

	sendError := mkSendError(ctx, stream)
	internalError := mkInternalError(ctx, stream)

	recvChan := mkRecvChan(ctx, stream)

	// TODO: Check if we are dealing with an existing session etc.
	r, ok := <-recvChan
	if !ok {
		return internalError("stream recv: channel closed")
	}
	if r.err != nil {
		return internalError("stream recv: %w", r.err)
	}

	if r.req.Cancel {
		return sendError("analysis canceled")
	}

	if r.req.Choice != nil {
		return sendError("premature choice")
	}

	if useDockerfile, err := s.useDockerfile(ctx, stream); useDockerfile || err != nil {
		return err
	}

	requirements_path := filepath.Join(s.SrcDir, "requirements.txt")

	if _, err := os.Stat(requirements_path); err != nil {
		ctx, span := trace.Span(ctx, "requirements")
		defer span.End()

		if !os.IsNotExist(err) {
			return internalError("stat requirements.txt: %w", err)
		}

		span.AddEvent("No requirements.txt")
		err := stream.Send(&pb.ActReply{
			Source: "ayup",
			Variant: &pb.ActReply_Choice{
				Choice: &pb.Choice{
					Variant: &pb.Choice_Bool{
						Bool: &pb.ChoiceBool{
							Value:       true,
							Title:       "No requirements.txt; try guessing it?",
							Description: "Guess what dependencies the program has by inspecting the source code.",
							Affirmative: "Yes, guess",
							Negative:    "No, I'll make it",
						},
					},
				},
			},
		})
		if err != nil {
			return internalError("stream send: %w", err)
		}

		span.AddEvent("Waiting for choice")
		r, ok := <-recvChan
		if !ok {
			return internalError("stream recv: channel closed")
		}
		if r.err != nil {
			return internalError("stream recv: %w", r.err)
		}

		if r.req.Cancel {
			return sendError("analysis canceled")
		}

		choice := r.req.Choice.GetBool()
		if choice == nil {
			return sendError("expected choice for requirements.txt")
		} else if !choice.Value {
			return sendError("can't continue without requirements.txt; please provide one!")
		}

		span.AddEvent("Creating requirements.txt")
		cmd := exec.Command("pipreqs", s.SrcDir)

		procWait := mkProcWaiter(ctx, stream, recvChan)
		in, out := startProc(ctx, cmd)

		if err = procWait("pipreqs", in, out); err != nil {
			return err
		}
	} else {
		span.AddEvent("requirements.txt exists")

		if err := stream.Send(&pb.ActReply{
			Source: "Ayup",
			Variant: &pb.ActReply_Log{
				Log: "requirements.txt found",
			},
		}); err != nil {
			return internalError("stream send: %w", err)
		}
	}

	s.push.analysis = &pb.AnalysisResult{
		UsePythonRequirements: true,
	}

	requirementsFile, err := os.OpenFile(requirements_path, os.O_RDONLY, 0)
	if err != nil {
		return internalError("open file: %w", err)
	}
	defer requirementsFile.Close()

	gitRegex := regexp.MustCompile(`@\s+git`)
	opencvRegex := regexp.MustCompile(`^\s*opencv-python\b`)
	lines := bufio.NewScanner(requirementsFile)
	for lines.Scan() {
		line := lines.Text()

		if gitRegex.MatchString(line) {
			s.push.analysis.NeedsGit = true
		}

		if opencvRegex.MatchString(line) {
			s.push.analysis.NeedsLibGL = true
			s.push.analysis.NeedsLibGlib = true
		}
	}

	if err := stream.Send(&pb.ActReply{
		Source: "Ayup",
		Variant: &pb.ActReply_AnalysisResult{
			AnalysisResult: s.push.analysis,
		},
	}); err != nil {
		return internalError("stream send: %w", err)
	}

	return nil
}
