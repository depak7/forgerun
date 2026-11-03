package main

import (
	"cmp"
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/depak7/forgerun/internal/handoff"
	"github.com/depak7/forgerun/internal/mcp"
)

// mcpCmd serves forgerun's MCP tools on stdin and stdout, for a local agent
// that starts it: handing work off to a forgerun server, checking on it,
// and fetching its changes. The token comes from the environment, so it's
// never on a command line other users of the machine can see.
func mcpCmd(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	server := fs.String("server", cmp.Or(os.Getenv("FORGERUN_URL"), "http://localhost:8080"), "the forgerun server's address (or FORGERUN_URL)")
	fs.Parse(args)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	c := &handoff.Client{URL: *server, Token: os.Getenv("FORGERUN_TOKEN")}
	return mcp.Serve(ctx, os.Stdin, os.Stdout, "forgerun", "1", handoff.Tools(c))
}
