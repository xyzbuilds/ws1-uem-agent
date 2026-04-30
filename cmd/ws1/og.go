package main

import (
	"time"

	"github.com/spf13/cobra"

	"github.com/zhangxuyang/ws1-uem-agent/internal/auth"
	"github.com/zhangxuyang/ws1-uem-agent/internal/envelope"
)

func newOgCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "og",
		Short: "Manage organization-group context (required for most ops)",
	}
	cmd.AddCommand(
		newOgUseCmd(),
		newOgCurrentCmd(),
		newOgClearCmd(),
	)
	return cmd
}

func newOgUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use <id>",
		Short: "Set the active OG ID",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			start := time.Now()
			if err := auth.SetOG(args[0]); err != nil {
				emitAndExit(envelope.NewError("ws1.og.use",
					envelope.CodeInternalError, err.Error()).WithDuration(time.Since(start)))
				return
			}
			emitAndExit(envelope.New("ws1.og.use").
				WithData(map[string]any{"og": args[0]}).
				WithDuration(time.Since(start)))
		},
	}
}

func newOgCurrentCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "current",
		Short: "Print the active OG ID",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			start := time.Now()
			id, err := auth.CurrentOG()
			if err != nil {
				emitAndExit(envelope.NewError("ws1.og.current",
					envelope.CodeInternalError, err.Error()).WithDuration(time.Since(start)))
				return
			}
			if id == "" {
				emitAndExit(envelope.NewError("ws1.og.current",
					envelope.CodeTenantRequired,
					"no OG context set; run `ws1 og use <id>` or pass --og").
					WithDuration(time.Since(start)))
				return
			}
			emitAndExit(envelope.New("ws1.og.current").
				WithData(map[string]any{"og": id}).
				WithDuration(time.Since(start)))
		},
	}
}

func newOgClearCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "clear",
		Short: "Clear the persisted default OG",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			start := time.Now()
			if err := auth.SetOG(""); err != nil {
				emitAndExit(envelope.NewError("ws1.og.clear",
					envelope.CodeInternalError, err.Error()).WithDuration(time.Since(start)))
				return
			}
			emitAndExit(envelope.New("ws1.og.clear").
				WithData(map[string]any{"cleared": true}).
				WithDuration(time.Since(start)))
		},
	}
}
