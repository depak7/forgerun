package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/depak7/forgerun/internal/sandbox"
)

func main() {
	ctx := context.Background()
	if err := sandbox.EnsureRuntime(ctx); err != nil {
		log.Fatal(err)
	}

	sb, err := sandbox.New(ctx, "hello", sandbox.Config{Image: "python", CPUs: 1, MemoryMiB: 512, MaxLifetime: 5 * time.Minute})
	if err != nil {
		log.Fatal(err)
	}
	defer sb.Destroy()

	// 1. Run: buffered output.
	out, err := sb.Run(ctx, sandbox.Cmd{Name: "python3", Args: []string{"-c", "print('Hello from Go!')"}})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Print(out)

	// 2. Stream: output appears line by line, one per second.
	err = sb.Stream(ctx, sandbox.Cmd{Name: "sh", Args: []string{"-c", "for i in 1 2 3; do echo tick $i; sleep 1; done"}}, os.Stdout)
	if err != nil {
		log.Fatal(err)
	}

	// 3. A failing command: before, this was silently "successful".
	_, err = sb.Run(ctx, sandbox.Cmd{Name: "ls", Args: []string{"/does-not-exist"}})
	var exitErr *sandbox.ExitError
	if errors.As(err, &exitErr) {
		fmt.Println("caught failure as expected → exit code", exitErr.Code, "|", exitErr)
	}
}
