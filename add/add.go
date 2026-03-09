package add

import (
	"claude-squad/config"
	"claude-squad/log"
	"claude-squad/session"
	"claude-squad/session/git"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var programFlag string

// Cmd is the cobra command for cs add
var Cmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Create a new session with the given name as branch",
	Long:  "Create a new claude-squad session. The name is used as both the session title and the git branch name (no prefix added).\nThe session is created in paused state. Open the TUI with 'cs' to start it.\nExample: cs add 1234-fix-login-bug",
	Args:  cobra.ExactArgs(1),
	RunE:  runAdd,
}

func init() {
	Cmd.Flags().StringVar(&programFlag, "program", "", "Program to run (default: config value)")
}

func runAdd(cmd *cobra.Command, args []string) error {
	log.Initialize(false)
	defer log.Close()

	branchName := args[0]
	// tmux session names cannot contain '/' or '.', replace for title
	title := strings.NewReplacer("/", "-", ".", "-").Replace(branchName)

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}

	program := programFlag
	if program == "" {
		cfg := config.LoadConfig()
		program = cfg.DefaultProgram
	}

	// Create worktree only (no tmux session).
	// The session will be started when opened in the TUI.
	gitWorktree, err := git.NewGitWorktreeFromBranch(cwd, branchName, title)
	if err != nil {
		return fmt.Errorf("failed to create git worktree: %w", err)
	}
	if err := gitWorktree.Setup(); err != nil {
		return fmt.Errorf("failed to setup git worktree: %w", err)
	}
	fmt.Printf("Created worktree for branch: %s\n", branchName)

	// Build InstanceData directly with Paused status
	instanceData := session.InstanceData{
		Title:   title,
		Path:    cwd,
		Branch:  branchName,
		Status:  session.Paused,
		Program: program,
		Worktree: session.GitWorktreeData{
			RepoPath:      gitWorktree.GetRepoPath(),
			WorktreePath:  gitWorktree.GetWorktreePath(),
			SessionName:   title,
			BranchName:    branchName,
			BaseCommitSHA: gitWorktree.GetBaseCommitSHA(),
		},
	}

	state := config.LoadState()
	storage, err := session.NewStorage(state)
	if err != nil {
		return fmt.Errorf("failed to create storage: %w", err)
	}
	if err := storage.AppendInstanceData(instanceData); err != nil {
		return fmt.Errorf("failed to save to state.json: %w", err)
	}

	fmt.Printf("Session '%s' saved (paused). Run 'cs' to start it in the TUI.\n", title)
	return nil
}
