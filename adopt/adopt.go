package adopt

import (
	"claude-squad/config"
	"claude-squad/log"
	"claude-squad/session"
	"claude-squad/session/git"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var (
	programFlag  string
	worktreeFlag string
)

// Cmd is the cobra command for cs adopt
var Cmd = &cobra.Command{
	Use:   "adopt [branch]",
	Short: "Adopt an existing branch into a claude-squad session",
	Long: `Adopt an existing git branch into a claude-squad managed session.
The branch must exist locally or on origin. If only on origin, it is fetched automatically.
The session starts in paused state.

Use --worktree to adopt an existing worktree (with uncommitted changes) by moving it
into the claude-squad managed directory.

Examples:
  cs adopt feature/login-fix
  cs adopt --worktree /path/to/worktree
  cs adopt my-branch --worktree /path/to/worktree`,
	Args: cobra.RangeArgs(0, 1),
	RunE: runAdopt,
}

func init() {
	Cmd.Flags().StringVar(&programFlag, "program", "", "Program to run (default: config value)")
	Cmd.Flags().StringVar(&worktreeFlag, "worktree", "", "Path to an existing worktree to adopt (moved into cs managed directory)")
}

func runAdopt(cmd *cobra.Command, args []string) error {
	log.Initialize(false)
	defer log.Close()

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}

	repoRoot, err := findRepoRoot(cwd)
	if err != nil {
		return err
	}

	program := programFlag
	if program == "" {
		cfg := config.LoadConfig()
		program = cfg.DefaultProgram
	}

	if worktreeFlag != "" {
		branchName := ""
		if len(args) > 0 {
			branchName = args[0]
		}
		return adoptExistingWorktree(repoRoot, branchName, program)
	}

	if len(args) == 0 {
		return fmt.Errorf("branch name is required (or use --worktree to auto-detect)")
	}
	branchName := args[0]
	title := strings.NewReplacer("/", "-", ".", "-").Replace(branchName)
	return adoptBranch(repoRoot, branchName, title, program)
}

// adoptBranch creates a new worktree from an existing branch (local or remote).
func adoptBranch(repoRoot, branchName, title, program string) error {
	// Check if the branch exists locally
	checkLocal := exec.Command("git", "-C", repoRoot, "show-ref", "--verify", fmt.Sprintf("refs/heads/%s", branchName))
	if err := checkLocal.Run(); err != nil {
		// Not local — check if it exists on origin and fetch it
		checkRemote := exec.Command("git", "-C", repoRoot, "show-ref", "--verify", fmt.Sprintf("refs/remotes/origin/%s", branchName))
		if err := checkRemote.Run(); err != nil {
			return fmt.Errorf("branch %q does not exist locally or on origin. Use 'cs add' to create a new branch", branchName)
		}
		// Create a local tracking branch from origin
		fetchCmd := exec.Command("git", "-C", repoRoot, "branch", branchName, fmt.Sprintf("origin/%s", branchName))
		if out, err := fetchCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to create local branch from origin/%s: %s", branchName, strings.TrimSpace(string(out)))
		}
		fmt.Printf("Fetched branch '%s' from origin.\n", branchName)
	}

	// Check if a worktree already exists for this branch
	if existingPath := findExistingWorktree(repoRoot, branchName); existingPath != "" {
		destPath, err := moveWorktreeToCsDir(repoRoot, existingPath, branchName)
		if err != nil {
			return err
		}
		baseCommitSHA := findMergeBase(repoRoot, branchName)
		return saveSession(repoRoot, title, branchName, program, repoRoot, destPath, baseCommitSHA)
	}

	// Create worktree from the existing branch
	gitWorktree, err := git.NewGitWorktreeFromBranch(repoRoot, branchName, title)
	if err != nil {
		return fmt.Errorf("failed to create git worktree: %w", err)
	}
	if err := gitWorktree.Setup(); err != nil {
		return fmt.Errorf("failed to setup git worktree: %w", err)
	}

	// Find the merge-base with HEAD for meaningful diffs
	baseCommitSHA := gitWorktree.GetBaseCommitSHA()
	if baseCommitSHA == "" {
		baseCommitSHA = findMergeBase(repoRoot, branchName)
	}

	return saveSession(repoRoot, title, branchName, program, gitWorktree.GetRepoPath(), gitWorktree.GetWorktreePath(), baseCommitSHA)
}

// findExistingWorktree checks if a worktree already exists for the given branch
// and returns its path, or empty string if not found.
func findExistingWorktree(repoRoot, branchName string) string {
	cmd := exec.Command("git", "-C", repoRoot, "worktree", "list", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}

	var currentPath string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "worktree ") {
			currentPath = strings.TrimPrefix(line, "worktree ")
		} else if strings.HasPrefix(line, "branch refs/heads/") {
			branch := strings.TrimPrefix(line, "branch refs/heads/")
			if branch == branchName && currentPath != repoRoot {
				return currentPath
			}
		}
	}
	return ""
}

// adoptExistingWorktree moves an existing worktree into cs managed directory.
// If branchName is empty, it is auto-detected from the worktree.
func adoptExistingWorktree(repoRoot, branchName, program string) error {
	worktreePath, err := filepath.Abs(worktreeFlag)
	if err != nil {
		return fmt.Errorf("failed to resolve worktree path: %w", err)
	}

	// Verify the path exists and is a directory
	info, err := os.Stat(worktreePath)
	if err != nil {
		return fmt.Errorf("worktree path %q does not exist: %w", worktreePath, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("worktree path %q is not a directory", worktreePath)
	}

	// Detect the branch from the worktree
	branchCmd := exec.Command("git", "-C", worktreePath, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := branchCmd.Output()
	if err != nil {
		return fmt.Errorf("path %q does not appear to be a git worktree", worktreePath)
	}
	worktreeBranch := strings.TrimSpace(string(out))

	if branchName == "" {
		branchName = worktreeBranch
	} else if worktreeBranch != branchName {
		return fmt.Errorf("worktree is on branch %q but you specified %q", worktreeBranch, branchName)
	}

	title := strings.NewReplacer("/", "-", ".", "-").Replace(branchName)

	destPath, err := moveWorktreeToCsDir(repoRoot, worktreePath, branchName)
	if err != nil {
		return err
	}

	baseCommitSHA := findMergeBase(repoRoot, branchName)
	return saveSession(repoRoot, title, branchName, program, repoRoot, destPath, baseCommitSHA)
}

// moveWorktreeToCsDir moves a worktree into the cs managed directory.
// If it's already in the cs directory, returns the path as-is.
func moveWorktreeToCsDir(repoRoot, worktreePath, branchName string) (string, error) {
	configDir, err := config.GetConfigDir()
	if err != nil {
		return "", fmt.Errorf("failed to get config directory: %w", err)
	}
	worktreesDir := filepath.Join(configDir, "worktrees")

	// Already in cs managed directory — no move needed
	if strings.HasPrefix(worktreePath, worktreesDir) {
		fmt.Printf("Reusing existing worktree at '%s'.\n", worktreePath)
		return worktreePath, nil
	}

	if err := os.MkdirAll(worktreesDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create worktrees directory: %w", err)
	}

	sanitizedBranch := strings.ReplaceAll(branchName, "/", "-")
	destPath := filepath.Join(worktreesDir, fmt.Sprintf("%s_%x", sanitizedBranch, time.Now().UnixNano()))

	moveCmd := exec.Command("git", "-C", repoRoot, "worktree", "move", worktreePath, destPath)
	if out, err := moveCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to move worktree: %s", strings.TrimSpace(string(out)))
	}
	fmt.Printf("Moved worktree from '%s' to '%s'.\n", worktreePath, destPath)
	return destPath, nil
}

// saveSession creates an InstanceData and persists it to state.json.
func saveSession(repoRoot, title, branch, program, repoPath, worktreePath, baseCommitSHA string) error {
	instanceData := session.InstanceData{
		Title:   title,
		Path:    repoRoot,
		Branch:  branch,
		Status:  session.Paused,
		Program: program,
		Worktree: session.GitWorktreeData{
			RepoPath:         repoPath,
			WorktreePath:     worktreePath,
			SessionName:      title,
			BranchName:       branch,
			BaseCommitSHA:    baseCommitSHA,
			IsExistingBranch: true,
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

	fmt.Printf("Adopted branch '%s' as session '%s' (paused).\n", branch, title)
	fmt.Println("NOTE: If the TUI is currently running, close and reopen it to see the new session.")
	return nil
}

// findMergeBase returns the merge-base of HEAD and the given branch, or empty string on failure.
func findMergeBase(repoRoot, branchName string) string {
	cmd := exec.Command("git", "-C", repoRoot, "merge-base", "HEAD", branchName)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// findRepoRoot resolves the git repository root from the given path.
func findRepoRoot(path string) (string, error) {
	cmd := exec.Command("git", "-C", path, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("not a git repository: %s", path)
	}
	return strings.TrimSpace(string(out)), nil
}
