package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/misfitdev/reynholm/config"
	"github.com/misfitdev/reynholm/google"
	"github.com/misfitdev/reynholm/reconcile"
	"github.com/misfitdev/reynholm/zitadel"
	"github.com/spf13/cobra"
)

const (
	envZitadelDomain = "ZITADEL_DOMAIN"
	envZitadelPAT    = "ZITADEL_PAT"
)

// Set by goreleaser via ldflags.
var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	cobra.CheckErr(rootCmd.Execute())
}

var rootCmd = &cobra.Command{
	Use:   "reynholm",
	Short: "Sync Google Workspace group memberships into ZITADEL project role grants",
	Long: `Reynholm reads group memberships from Google Workspace, maps them to
ZITADEL project roles, and corrects drift. Runs statelessly: reads current
state from both systems, diffs, and applies changes when --apply is set.`,
	Version:      version,
	SilenceUsage: true,
	RunE:         run,
}

func init() {
	rootCmd.PersistentFlags().StringP("config", "c", "", "path to YAML config file (required)")
	rootCmd.PersistentFlags().Bool("apply", false, "apply changes to ZITADEL")
	rootCmd.PersistentFlags().Bool("dry-run", false, "preview changes only; overrides --apply when both are set")
	rootCmd.PersistentFlags().String("log-level", "info", "log level: debug|info|warn|error")
	rootCmd.PersistentFlags().String("log-format", "text", "log format: text|json")
	_ = rootCmd.MarkPersistentFlagRequired("config")
	rootCmd.SetVersionTemplate(fmt.Sprintf("reynholm %s (%s)\n", version, commit))
}

func run(cmd *cobra.Command, _ []string) error {
	configPath, _ := cmd.Flags().GetString("config")
	apply, _ := cmd.Flags().GetBool("apply")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	logLevel, _ := cmd.Flags().GetString("log-level")
	logFormat, _ := cmd.Flags().GetString("log-format")

	logger := newLogger(os.Stderr, logLevel, logFormat)
	slog.SetDefault(logger)

	// dry-run wins when both are set: a human passing --apply --dry-run almost
	// certainly wants the safer path.
	effectiveApply := apply && !dryRun
	mode := "dry-run"
	if effectiveApply {
		mode = "apply"
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load(configPath)
	if err != nil {
		logger.Error("config load failed", "err", err, "path", configPath)
		return fmt.Errorf("config load: %w", err)
	}

	logger.Info(
		"reynholm started",
		"mode", mode,
		"config", configPath,
		"google_domain", cfg.GoogleDomain,
		"managed_group", cfg.ManagedGroup,
		"projects", len(cfg.Projects),
	)

	for _, p := range cfg.Projects {
		logger.Debug("project loaded", "id", p.ID, "groups", p.Groups)
	}

	zitadelDomain := os.Getenv(envZitadelDomain)
	zitadelPAT := os.Getenv(envZitadelPAT)
	if zitadelDomain == "" || zitadelPAT == "" {
		logger.Error("missing zitadel credentials",
			"env_domain", envZitadelDomain, "env_pat", envZitadelPAT)
		return fmt.Errorf("missing required environment variables: %s, %s", envZitadelDomain, envZitadelPAT)
	}

	googleClient, err := google.NewClient(ctx)
	if err != nil {
		logger.Error("google client init failed", "err", err)
		return fmt.Errorf("google client: %w", err)
	}

	zitadelClient, err := zitadel.NewClient(ctx, zitadelDomain, zitadelPAT)
	if err != nil {
		logger.Error("zitadel client init failed", "err", err, "domain", zitadelDomain)
		return fmt.Errorf("zitadel client: %w", err)
	}
	defer func() {
		if err := zitadelClient.Close(); err != nil {
			logger.Warn("zitadel client close failed", "err", err)
		}
	}()

	r := reconcile.New(googleClient, zitadelClient, cfg, logger)
	if err := r.Run(ctx, !effectiveApply); err != nil {
		logger.Error("reconcile failed", "err", err, "mode", mode)
		return fmt.Errorf("reconcile: %w", err)
	}

	select {
	case <-ctx.Done():
		logger.Warn("interrupted", "cause", ctx.Err())
	default:
	}

	logger.Info("reynholm finished", "mode", mode)
	return nil
}

func newLogger(w *os.File, level, format string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler
	if format == "json" {
		h = slog.NewJSONHandler(w, opts)
	} else {
		h = slog.NewTextHandler(w, opts)
	}
	return slog.New(h).With(slog.String("app", "reynholm"))
}
