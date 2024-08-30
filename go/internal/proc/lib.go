package proc

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"sync"

	"premai.io/Ayup/go/internal/terror"

	attr "go.opentelemetry.io/otel/attribute"
	tr "go.opentelemetry.io/otel/trace"
)

type Out struct {
	Err error
	Ret *int
}

type In struct {
	Stdio []byte
}

func Start(ctx context.Context, cmd *exec.Cmd) (chan<- In, <-chan Out) {
	span := tr.SpanFromContext(ctx)
	procInChan := make(chan In, 1)
	procOutChan := make(chan Out, 1)

	errOut := func(err error) (chan<- In, <-chan Out) {
		procOutChan <- Out{
			Err: err,
		}

		close(procOutChan)

		return procInChan, procOutChan
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return errOut(terror.Errorf(ctx, "stdin pipe: %w", err))
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return errOut(terror.Errorf(ctx, "stdout pipe: %w", err))
	}
	outreader := bufio.NewReader(stdout)

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return errOut(terror.Errorf(ctx, "stderr pipe: %w", err))
	}
	errreader := bufio.NewReader(stderr)

	var wg sync.WaitGroup
	wg.Add(2)

	readOut := func(reader *bufio.Reader) {
		defer wg.Done()

		scanner := bufio.NewScanner(reader)

		for scanner.Scan() {
			text := scanner.Text()
			span.AddEvent("log", tr.WithAttributes(attr.String("text", text)))
		}
	}

	go readOut(outreader)
	go readOut(errreader)

	err = cmd.Start()
	if err != nil {
		return errOut(terror.Errorf(ctx, "cmd start: %w", err))
	}

	procDone := make(chan struct{})

	go func() {
		defer close(procOutChan)

		go func(ctx context.Context) {
			err = cmd.Wait()
			exitCode := 0
			if err != nil {
				if exitError, ok := err.(*exec.ExitError); ok {
					exitCode = exitError.ExitCode()
				}
				err = terror.Errorf(ctx, "cmd wait: %w", err)
			}

			// send the logs before the exit code
			wg.Wait()
			procOutChan <- Out{
				Err: err,
				Ret: &exitCode,
			}
			close(procDone)
		}(ctx)

		termProc := func() {
			if cmd.Process == nil {
				return
			}

			if err := cmd.Process.Signal(os.Interrupt); err != nil {
				_ = terror.Errorf(ctx, "signal interrupt: %w", err)
			}
		}

		span.AddEvent("Started proc")
	loop:
		for {
			select {
			case r := <-procInChan:
				if r.Stdio != nil {
					if _, err := stdin.Write(r.Stdio); err != nil {
						procOutChan <- Out{
							Err: terror.Errorf(ctx, "stdin write: %w", err),
						}
						termProc()
					}

					if err := stdin.Close(); err != nil {
						procOutChan <- Out{
							Err: terror.Errorf(ctx, "stdin close: %w", err),
						}
						termProc()
					}
				}
			case <-ctx.Done():
				termProc()
			case <-procDone:
				break loop
			}
		}
	}()

	return procInChan, procOutChan
}
