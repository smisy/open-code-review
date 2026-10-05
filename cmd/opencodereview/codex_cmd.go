// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/alibaba/open-code-review/internal/llm/codex"
	"github.com/spf13/cobra"
)

var codexCmd = &cobra.Command{
	Use: "codex", Short: "Use a ChatGPT subscription for OpenAI Codex reviews",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error { return cmd.Help() },
}

var codexLogin = func(ctx context.Context, in io.Reader, out, stderr io.Writer, home string) error {
	command := exec.CommandContext(ctx, "codex", "login", "--device-auth", "-c", `cli_auth_credentials_store="file"`)
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "CODEX_HOME=") {
			command.Env = append(command.Env, v)
		}
	}
	command.Env = append(command.Env, "CODEX_HOME="+home)
	command.Stdin, command.Stdout, command.Stderr = in, out, stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("Codex login failed (install the official Codex CLI first): %w", err)
	}
	return nil
}

func init() {
	codexCmd.AddCommand(&cobra.Command{Use: "login", Short: "Sign in with ChatGPT using the official Codex CLI", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := codex.Home()
			if err != nil {
				return err
			}
			if err := os.MkdirAll(home, 0700); err != nil {
				return err
			}
			unlock, err := lockCodexLogin(home)
			if err != nil {
				return err
			}
			defer unlock()
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()
			if err := codexLogin(ctx, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr(), home); err != nil {
				return err
			}
			path := filepath.Join(home, "auth.json")
			if _, err := codex.Load(path); err != nil {
				return err
			}
			if err := os.Chmod(path, 0600); err != nil {
				return err
			}
			cfgPath, err := defaultConfigPath()
			if err != nil {
				return err
			}
			return selectCodexProvider(cmd.OutOrStdout(), cfgPath)
		}})
	codexCmd.AddCommand(&cobra.Command{Use: "status", Short: "Check the stored ChatGPT login", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := codex.Home()
			if err != nil {
				return err
			}
			if _, err := codex.Load(filepath.Join(home, "auth.json")); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "ChatGPT login is stored. Select 'openai-codex' with 'ocr config set provider openai-codex', then run 'ocr llm test' to verify access.")
			return nil
		}})
	codexCmd.AddCommand(&cobra.Command{Use: "logout", Short: "Remove OCR's stored ChatGPT login", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := codex.Home()
			if err != nil {
				return err
			}
			if err := os.MkdirAll(home, 0700); err != nil {
				return err
			}
			unlock, err := lockCodexLogin(home)
			if err != nil {
				return err
			}
			defer unlock()
			if err := os.Remove(filepath.Join(home, "auth.json")); err != nil && !os.IsNotExist(err) {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Removed OCR's ChatGPT login.")
			return nil
		}})
}

func selectCodexProvider(out io.Writer, path string) error {
	cfg, err := loadOrCreateConfig(path)
	if err != nil {
		return err
	}
	if cfg.Providers == nil {
		cfg.Providers = map[string]ProviderEntry{}
	}
	entry := cfg.Providers["openai-codex"]
	if entry.Model == "" {
		if cfg.Provider == "openai-codex" && cfg.Model != "" {
			entry.Model = cfg.Model
		} else {
			entry.Model = "gpt-6.1-sol"
		}
	}
	cfg.Providers["openai-codex"] = entry
	if cfg.Provider == "" {
		cfg.Provider = "openai-codex"
		cfg.Model = ""
	}
	if err := saveConfig(path, cfg); err != nil {
		return err
	}
	fmt.Fprintln(out, "Signed in with ChatGPT. Select this provider with: ocr config set provider openai-codex")
	return nil
}

func lockCodexLogin(home string) (func(), error) {
	path := filepath.Join(home, "auth.json.lock")
	if err := os.Mkdir(path, 0700); err != nil {
		return nil, fmt.Errorf("Codex login is in use; retry when the other OCR command finishes: %w", err)
	}
	return func() { os.Remove(path) }, nil
}
