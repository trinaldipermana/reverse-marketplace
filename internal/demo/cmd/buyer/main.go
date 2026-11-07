package main

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/trinaldirizki/reverse-marketplace/internal/demo"
)

const usage = `Usage
  scripts/demo.sh reset
  scripts/demo.sh post <scenario>
  scripts/demo.sh thread <scenario>
  scripts/demo.sh reply <scenario> <step> [agent-message-id]

Scenarios are purchase, exception, and non_buying.
DEMO_BASE_URL defaults to http://127.0.0.1:8080.
Set DEMO_RUN_AT once per run in RFC3339 before publishing a post.
Set DEMO_RESET_TOKEN to the local operator token before reset.
`

func main() {
	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	if err := run(context.Background(), os.Args[1:], os.DirFS("."), client, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, files fs.FS, client *http.Client, output io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprint(output, usage)
		return err
	}
	baseURL := os.Getenv("DEMO_BASE_URL")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8080"
	}
	buyer := demo.Buyer{BaseURL: baseURL, Client: client}
	if args[0] == "reset" && len(args) == 1 {
		if err := buyer.Reset(ctx, os.Getenv("DEMO_RESET_TOKEN")); err != nil {
			return err
		}
		_, err := fmt.Fprintln(output, "Demo reset complete.")
		return err
	}
	if len(args) < 2 {
		return fmt.Errorf("provide a scenario or run scripts/demo.sh --help")
	}
	scenario, err := demo.LoadScenario(files, args[1])
	if err != nil {
		return err
	}

	var body []byte
	switch {
	case args[0] == "post" && len(args) == 2:
		at, parseErr := time.Parse(time.RFC3339, os.Getenv("DEMO_RUN_AT"))
		if parseErr != nil {
			return fmt.Errorf("set DEMO_RUN_AT to the run's RFC3339 timestamp before publishing")
		}
		body, err = buyer.Publish(ctx, scenario.Post, at)
	case args[0] == "thread" && len(args) == 2:
		body, err = buyer.Thread(ctx, scenario.Post.ThreadID)
	case args[0] == "reply" && (len(args) == 3 || len(args) == 4):
		reply, ok := scenario.Replies[args[2]]
		if !ok {
			return fmt.Errorf("scenario %q has no buyer step %q", args[1], args[2])
		}
		var replyTo string
		if len(args) == 4 {
			replyTo = args[3]
		}
		body, err = buyer.Reply(ctx, scenario.Post.ThreadID, reply, replyTo)
	default:
		return fmt.Errorf("invalid command arguments. Run scripts/demo.sh --help")
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, strings.TrimSpace(string(body)))
	return err
}
