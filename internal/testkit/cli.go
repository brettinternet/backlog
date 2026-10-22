package testkit

import (
	"bytes"
	"context"
	"io"
)

// RunCLI captures an injected-stream runner without changing process streams.
func RunCLI(ctx context.Context, args []string, runner func(context.Context, []string, io.Writer, io.Writer) int) (int, string, string) {
	var out, errOut bytes.Buffer
	code := runner(ctx, append([]string(nil), args...), &out, &errOut)
	return code, out.String(), errOut.String()
}
