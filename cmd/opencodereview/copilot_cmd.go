// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/alibaba/open-code-review/internal/llm"
	"github.com/alibaba/open-code-review/internal/llm/copilot"
	"github.com/spf13/cobra"
)

const copilotProviderName = "github-copilot"

var copilotCmd = &cobra.Command{
	Use:   "copilot",
	Short: "Use a GitHub Copilot subscription as the LLM provider",
	Long: `Use a GitHub Copilot subscription as the LLM provider.

'ocr copilot login' signs in with the GitHub device flow and stores the GitHub
token in ~/.opencodereview/github-copilot.json. The github-copilot provider
exchanges it for short-lived Copilot API tokens during each run. The
COPILOT_GITHUB_TOKEN environment variable, or providers.github-copilot.api_key,
takes precedence over the stored login.`,
	Example: `  ocr copilot login              Sign in and select the github-copilot provider
  ocr copilot models             List the models your Copilot plan allows
  ocr copilot status             Check that the credential works
  ocr copilot logout             Remove the stored login`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

var copilotLoginCmd = &cobra.Command{
	Use:   "login",
	Short: "Sign in to GitHub Copilot with the device flow",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
		defer stop()
		cfgPath, err := defaultConfigPath()
		if err != nil {
			return err
		}
		return runCopilotLogin(ctx, cmd.OutOrStdout(), cfgPath)
	},
}

var copilotLogoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Remove the stored GitHub Copilot login",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runCopilotLogout(cmd.OutOrStdout())
	},
}

var copilotStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check the GitHub Copilot credential",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runCopilotStatus(cmd.Context(), cmd.OutOrStdout())
	},
}

var copilotModelsCmd = &cobra.Command{
	Use:   "models",
	Short: "List the chat models your Copilot plan allows",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runCopilotModels(cmd.Context(), cmd.OutOrStdout())
	},
}

func init() {
	copilotCmd.AddCommand(copilotLoginCmd, copilotLogoutCmd, copilotStatusCmd, copilotModelsCmd)
}

// Swapped in tests, which cannot reach GitHub.
var (
	copilotRequestDeviceCode = copilot.RequestDeviceCode
	copilotPollAccessToken   = copilot.PollAccessToken
	copilotCheckToken        = func(ctx context.Context, githubToken string) (string, error) {
		_, baseURL, err := copilot.NewTokenSource(githubToken).Token(ctx)
		return baseURL, err
	}
	copilotListModels = func(ctx context.Context, githubToken string) ([]copilot.Model, error) {
		return copilot.ListModels(ctx, copilot.NewTokenSource(githubToken))
	}
)

func runCopilotLogin(ctx context.Context, out io.Writer, cfgPath string) error {
	dc, err := copilotRequestDeviceCode(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Open %s and enter the code: %s\n", dc.VerificationURI, dc.UserCode)
	fmt.Fprintf(out, "Waiting for approval (the code expires in %s)...\n", time.Duration(dc.ExpiresIn)*time.Second)
	token, err := copilotPollAccessToken(ctx, dc)
	if err != nil {
		return err
	}
	if _, err := copilotCheckToken(ctx, token); err != nil {
		return fmt.Errorf("signed in to GitHub, but Copilot is not available: %w", err)
	}
	if err := copilot.SaveGitHubToken(token); err != nil {
		return fmt.Errorf("save login: %w", err)
	}
	path, _ := copilot.CredentialsPath()
	fmt.Fprintf(out, "Signed in. Credential saved to %s\n", path)
	return selectCopilotProvider(out, cfgPath)
}

// selectCopilotProvider makes github-copilot the active provider unless the
// user already configured a different one, which a login must not override.
func selectCopilotProvider(out io.Writer, cfgPath string) error {
	cfg, err := loadOrCreateConfig(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	keepOther := cfg.Provider != "" && cfg.Provider != copilotProviderName
	if cfg.Providers == nil {
		cfg.Providers = map[string]ProviderEntry{}
	}
	entry := cfg.Providers[copilotProviderName]
	// The top-level model belongs to whichever provider is active, so it only
	// counts when that provider is github-copilot. The entry model is what
	// lets 'ocr config set provider github-copilot' work later on its own,
	// since that command clears the top-level model.
	if entry.Model == "" && (keepOther || cfg.Model == "") {
		entry.Model = "claude-sonnet-5"
	}
	cfg.Providers[copilotProviderName] = entry
	if !keepOther {
		cfg.Provider = copilotProviderName
	}
	if err := saveConfig(cfgPath, cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	if keepOther {
		fmt.Fprintf(out, "Your active provider is %q. Switch with: ocr config set provider %s (model %s)\n", cfg.Provider, copilotProviderName, entry.Model)
		return nil
	}
	model := entry.Model
	if model == "" {
		model = cfg.Model
	}
	fmt.Fprintf(out, "Provider set to %s (model %s). Change the model with: ocr config set model <id>\n", copilotProviderName, model)
	return nil
}

func runCopilotLogout(out io.Writer) error {
	removed, err := copilot.DeleteGitHubToken()
	if err != nil {
		return err
	}
	if removed {
		fmt.Fprintln(out, "Removed the stored GitHub Copilot login.")
	} else {
		fmt.Fprintln(out, "No stored GitHub Copilot login.")
	}
	if os.Getenv(copilot.EnvToken) != "" {
		fmt.Fprintf(out, "Note: $%s is still set and will be used.\n", copilot.EnvToken)
	}
	return nil
}

// copilotGitHubToken resolves the credential a review would use, so status and
// models report on the same token.
var copilotGitHubToken = func() (token, source string, err error) {
	cfgPath, err := defaultConfigPath()
	if err != nil {
		return "", "", err
	}
	return llm.ResolveCopilotCredential(cfgPath)
}

func runCopilotStatus(ctx context.Context, out io.Writer) error {
	token, source, err := copilotGitHubToken()
	if err != nil {
		return err
	}
	baseURL, err := copilotCheckToken(ctx, token)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Credential: %s\nCopilot API: %s\nStatus: OK\n", source, baseURL)
	return nil
}

func runCopilotModels(ctx context.Context, out io.Writer) error {
	token, _, err := copilotGitHubToken()
	if err != nil {
		return err
	}
	models, err := copilotListModels(ctx, token)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "MODEL\tVENDOR\tTOOLS\tPROTOCOL\tENDPOINTS")
	for _, m := range models {
		tools := "no"
		if m.ToolCalls {
			tools = "yes"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", m.ID, m.Vendor, tools, copilot.ProtocolForModel(m.ID), strings.Join(m.Endpoints, ","))
	}
	return tw.Flush()
}
