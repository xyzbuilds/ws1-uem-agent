package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/zhangxuyang/ws1-uem-agent/internal/api"
	"github.com/zhangxuyang/ws1-uem-agent/internal/envelope"
)

func newUsersCmd() *cobra.Command {
	systemv2 := &cobra.Command{
		Use:   "systemv2",
		Short: "System API V2 commands",
	}
	users := &cobra.Command{
		Use:   "users",
		Short: "Enrolled users",
	}
	users.AddCommand(newUsersSearchCmd(), newUsersGetCmd())
	systemv2.AddCommand(users)
	return systemv2
}

func newUsersSearchCmd() *cobra.Command {
	var username, email, firstname, lastname string
	var page, pageSize int
	cmd := &cobra.Command{
		Use:   "search",
		Short: "Search enrolled users",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			start := time.Now()
			cli, og, errEnv := buildAPIClient()
			if errEnv != nil {
				emitAndExit(errEnv.WithDuration(time.Since(start)))
				return
			}
			a := api.Args{
				"page":     page,
				"pagesize": pageSize,
			}
			if username != "" {
				a["username"] = username
			}
			if email != "" {
				a["email"] = email
			}
			if firstname != "" {
				a["firstname"] = firstname
			}
			if lastname != "" {
				a["lastname"] = lastname
			}
			if og != "" {
				a["organizationgroupid"] = og
			}
			resp, err := cli.Do(context.Background(), "systemv2.users.search", a)
			if err != nil {
				emitAndExit(envelope.NewError("systemv2.users.search",
					envelope.CodeNetworkError, err.Error()).WithDuration(time.Since(start)))
				return
			}
			if resp.StatusCode >= 400 {
				emitAndExit(httpErrorEnvelope("systemv2.users.search", resp).
					WithDuration(time.Since(start)))
				return
			}
			var parsed struct {
				Users    []map[string]any `json:"Users"`
				Page     int              `json:"Page"`
				PageSize int              `json:"PageSize"`
				Total    int              `json:"Total"`
			}
			if err := resp.JSON(&parsed); err != nil {
				emitAndExit(envelope.NewError("systemv2.users.search",
					envelope.CodeInternalError, err.Error()).WithDuration(time.Since(start)))
				return
			}
			hasMore := parsed.Page*parsed.PageSize+len(parsed.Users) < parsed.Total
			emitAndExit(envelope.New("systemv2.users.search").
				WithData(parsed.Users).
				WithPagination(parsed.Total, parsed.Page, parsed.PageSize, hasMore).
				WithDuration(time.Since(start)))
		},
	}
	cmd.Flags().StringVar(&username, "username", "", "")
	cmd.Flags().StringVar(&email, "email", "", "")
	cmd.Flags().StringVar(&firstname, "firstname", "", "")
	cmd.Flags().StringVar(&lastname, "lastname", "", "")
	cmd.Flags().IntVar(&page, "page", 0, "page number (0-indexed)")
	cmd.Flags().IntVar(&pageSize, "pagesize", 100, "page size")
	return cmd
}

func newUsersGetCmd() *cobra.Command {
	var id int
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Get a user by ID",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			start := time.Now()
			cli, _, errEnv := buildAPIClient()
			if errEnv != nil {
				emitAndExit(errEnv.WithDuration(time.Since(start)))
				return
			}
			if id == 0 {
				emitAndExit(envelope.NewError("systemv2.users.get",
					envelope.CodeIdentifierAmbiguous, "--id is required").
					WithDuration(time.Since(start)))
				return
			}
			resp, err := cli.Do(context.Background(), "systemv2.users.get",
				api.Args{"id": id})
			if err != nil {
				emitAndExit(envelope.NewError("systemv2.users.get",
					envelope.CodeNetworkError, err.Error()).WithDuration(time.Since(start)))
				return
			}
			if resp.StatusCode == 404 {
				emitAndExit(envelope.NewError("systemv2.users.get",
					envelope.CodeIdentifierNotFound,
					fmt.Sprintf("no user with id %d", id)).
					WithDuration(time.Since(start)))
				return
			}
			if resp.StatusCode >= 400 {
				emitAndExit(httpErrorEnvelope("systemv2.users.get", resp).
					WithDuration(time.Since(start)))
				return
			}
			var user map[string]any
			if err := resp.JSON(&user); err != nil {
				emitAndExit(envelope.NewError("systemv2.users.get",
					envelope.CodeInternalError, err.Error()).WithDuration(time.Since(start)))
				return
			}
			emitAndExit(envelope.New("systemv2.users.get").
				WithData(user).
				WithDuration(time.Since(start)))
		},
	}
	cmd.Flags().IntVar(&id, "id", 0, "user ID")
	return cmd
}
