package main

import (
	"time"

	"github.com/spf13/cobra"

	"github.com/zhangxuyang/ws1-uem-agent/internal/audit"
	"github.com/zhangxuyang/ws1-uem-agent/internal/envelope"
)

func newAuditCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Inspect the local hash-chained audit log",
		Long: `Every state-changing op writes a JSONL entry to ~/.config/ws1/audit.log
with a SHA-256 chain so tampering is detectable. v1 limitation: the file
is writable by the agent's OS user (CLAUDE.md locked decision #10).`,
	}
	cmd.AddCommand(newAuditTailCmd(), newAuditVerifyCmd())
	return cmd
}

func newAuditTailCmd() *cobra.Command {
	var n int
	cmd := &cobra.Command{
		Use:   "tail",
		Short: "Print the last N entries (default 10)",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			start := time.Now()
			path, err := audit.DefaultPath()
			if err != nil {
				emitAndExit(envelope.NewError("ws1.audit.tail",
					envelope.CodeInternalError, err.Error()).WithDuration(time.Since(start)))
				return
			}
			l, err := audit.New(path)
			if err != nil {
				emitAndExit(envelope.NewError("ws1.audit.tail",
					envelope.CodeInternalError, err.Error()).WithDuration(time.Since(start)))
				return
			}
			entries, err := l.Tail(n)
			if err != nil {
				emitAndExit(envelope.NewError("ws1.audit.tail",
					envelope.CodeInternalError, err.Error()).WithDuration(time.Since(start)))
				return
			}
			emitAndExit(envelope.New("ws1.audit.tail").
				WithData(map[string]any{
					"entries": entries,
					"count":   len(entries),
					"path":    path,
				}).
				WithDuration(time.Since(start)))
		},
	}
	cmd.Flags().IntVar(&n, "last", 10, "number of entries to return; 0 means all")
	return cmd
}

func newAuditVerifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "verify",
		Short: "Verify the hash chain end-to-end",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			start := time.Now()
			path, err := audit.DefaultPath()
			if err != nil {
				emitAndExit(envelope.NewError("ws1.audit.verify",
					envelope.CodeInternalError, err.Error()).WithDuration(time.Since(start)))
				return
			}
			l, _ := audit.New(path)
			rep, err := l.Verify()
			if err != nil {
				emitAndExit(envelope.NewError("ws1.audit.verify",
					envelope.CodeInternalError, err.Error()).WithDuration(time.Since(start)))
				return
			}
			env := envelope.New("ws1.audit.verify").
				WithData(map[string]any{
					"total":    rep.Total,
					"ok":       rep.OK,
					"failures": rep.Failures,
					"path":     path,
				}).
				WithDuration(time.Since(start))
			if !rep.OK {
				env = envelope.NewError("ws1.audit.verify",
					envelope.CodeInternalError,
					"audit chain verification failed").
					WithErrorDetails(map[string]any{
						"total":    rep.Total,
						"failures": rep.Failures,
						"path":     path,
					}).
					WithDuration(time.Since(start))
			}
			emitAndExit(env)
		},
	}
}
