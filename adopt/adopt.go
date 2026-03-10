package adopt

import (
	"claude-squad/config"
	"claude-squad/log"
	"claude-squad/session"
	"claude-squad/session/git"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

var programFlag string

// Cmd is the cobra command for cs adopt
var Cmd = &cobra.Command{
	Use:   "adopt <branch>",
	Short: "Adopt an existing branch into a claude-squad session",
	Long: `Adopt an existing git branch (without a local worktree) into a claude-squad managed session.
The branch must exist locally or on origin. If only on origin, it is fetched automatically.
The session starts in paused state.
Example: cs adopt feature/login-fix`,
	Args: cobra.ExactArgs(1),
	RunE: runAdopt,
}

func init() {
	Cmd.Flags().StringVar(&programFlag, "program", "", "Program to run (default: config value)")
}

func runAdopt(cmd *cobra.Command, args []string) error {
	log.Initialize(false)
	defer log.Close()

	branchName := args[0]

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}

	// Check if the branch exists locally
	checkLocal := exec.Command("git", "-C", cwd, "show-ref", "--verify", fmt.Sprintf("refs/heads/%s", branchName))
	if err := checkLocal.Run(); err != nil {
		// Not local — check if it exists on origin and fetch it
		checkRemote := exec.Command("git", "-C", cwd, "show-ref", "--verify", fmt.Sprintf("refs/remotes/origin/%s", branchName))
		if err := checkRemote.Run(); err != nil {
			return fmt.Errorf("branch %q does not exist locally or on origin. Use 'cs add' to create a new branch", branchName)
		}
		// Create a local tracking branch from origin
		fetchCmd := exec.Command("git", "-C", cwd, "branch", branchName, fmt.Sprintf("origin/%s", branchName))
		if out, err := fetchCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to create local branch from origin/%s: %s", branchName, strings.TrimSpace(string(out)))
		}
		fmt.Printf("Fetched branch '%s' from origin.\n", branchName)
	}

	// tmux session names cannot contain '/' or '.', replace for title
	title := strings.NewReplacer("/", "-", ".", "-").Replace(branchName)

	program := programFlag
	if program == "" {
		cfg := config.LoadConfig()
		program = cfg.DefaultProgram
	}

	// Create worktree from the existing branch
	gitWorktree, branch, err := git.NewGitWorktreeWithBranch(cwd, title, branchName)
	if err != nil {
		return fmt.Errorf("failed to create git worktree: %w", err)
	}
	if err := gitWorktree.Setup(); err != nil {
		return fmt.Errorf("failed to setup git worktree: %w", err)
	}

	// Find the merge-base with HEAD for meaningful diffs
	baseCommitSHA := gitWorktree.GetBaseCommitSHA()
	if baseCommitSHA == "" {
		// setupFromExistingBranch doesn't set baseCommitSHA, so compute it
		mergeBaseCmd := exec.Command("git", "-C", cwd, "merge-base", "HEAD", branchName)
		if out, err := mergeBaseCmd.Output(); err == nil {
			baseCommitSHA = strings.TrimSpace(string(out))
		}
	}

	// Build InstanceData directly with Paused status
	instanceData := session.InstanceData{
		Title:   title,
		Path:    cwd,
		Branch:  branch,
		Status:  session.Paused,
		Program: program,
		Worktree: session.GitWorktreeData{
			RepoPath:      gitWorktree.GetRepoPath(),
			WorktreePath:  gitWorktree.GetWorktreePath(),
			SessionName:   title,
			BranchName:    branch,
			BaseCommitSHA: baseCommitSHA,
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

	fmt.Printf("Adopted branch '%s' as session '%s' (paused). Run 'cs' to start it in the TUI.\n", branch, title)
	return nil
}
