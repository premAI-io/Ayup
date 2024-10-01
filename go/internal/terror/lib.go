package terror

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	tr "premai.io/Ayup/go/internal/trace"
)

func Errorf(ctx context.Context, format string, a ...any) error {
	err := fmt.Errorf(format, a...)
	tr.Zlog.Error(err.Error())
	_ = tr.Zlog.Sync()

	span := trace.SpanFromContext(ctx)
	span.RecordError(err, trace.WithStackTrace(true))
	span.SetStatus(codes.Error, err.Error())

	return err
}

func Ackf(ctx context.Context, format string, err error) {
	if err != nil {
		_ = Errorf(ctx, format, err)
	}
}
