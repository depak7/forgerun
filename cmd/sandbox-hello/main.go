package main

import (
	"context"
	"fmt"
	"log"

	microsandbox "github.com/superradcompany/microsandbox/sdk/go"
)

func main() {
	ctx := context.Background()

	if _, err := microsandbox.EnsureRuntime(ctx, microsandbox.RuntimeConfig{}, microsandbox.InstallOptions{}); err != nil {
		log.Fatal(err)
	}

	sandbox, err := microsandbox.CreateSandbox(ctx, "lesson2",
		microsandbox.WithImage("python"),
		microsandbox.WithCPUs(1),
		microsandbox.WithMemory(512),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer sandbox.Stop(ctx)

	output, err := sandbox.Exec(ctx, "python3", []string{"-c", "print('Hello from Go!')"})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(output.Stdout())
}
