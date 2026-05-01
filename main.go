package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Constants for application configuration
const (
	refHistoryFile = ".ref-history"           // File to track merge history
	githubAPI      = "https://api.github.com" // GitHub API endpoint
	userAgent      = "GitHubMergeBot/1.0"     // User agent for API requests
)

// Config holds application configuration parameters
type Config struct {
	GithubToken    string   `json:"github_token"`    // GitHub access token
	Owner          string   `json:"owner"`           // Repository owner
	Repo           string   `json:"repo"`            // Repository name
	TrunkBranch    string   `json:"trunk_branch"`    // Base branch (usually main/master)
	TargetBranch   string   `json:"target_branch"`   // Target branch for merges
	RequiredLabels []string `json:"required_labels"` // Required PR labels
	GitHubOutput   string   `json:"github_output"`   // GitHub output path
}

// RefHistory tracks merged pull requests
type RefHistory struct {
	Merges []MergeRecord `json:"merges"` // List of merge records
}

// MergeRecord represents a single merged PR
type MergeRecord struct {
	PR        int       `json:"pr"`        // Pull Request number
	Commit    string    `json:"commit"`    // Resulting commit SHA
	Timestamp time.Time `json:"timestamp"` // Merge timestamp
}

// ConflictError represents a squash merge failure caused by file conflicts
type ConflictError struct {
	Files     []string // conflicting file paths
	GitOutput string   // raw output from git merge --squash, shown directly to the user
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("merge conflict in %d file(s)", len(e.Files))
}

// ErrEmptyMerge signals that a PR produced no new changes after squash merge.
// This means the PR's changes are already present in the target branch.
var ErrEmptyMerge = errors.New("PR changes are already included in the target branch")

// GitHubPR represents a simplified Pull Request structure
type GitHubPR struct {
	Number    int    `json:"number"`     // PR number
	Title     string `json:"title"`      // PR title
	State     string `json:"state"`      // PR state (open/closed)
	CreatedAt string `json:"created_at"` // PR createAt
	Base      struct {
		Ref string `json:"ref"` // Base branch reference
	} `json:"base"`
	Labels []string `json:"labels"` // List of PR labels
}

func main() {
	cfg := mustParseConfig()
	defer setOutput(cfg, "target_branch", cfg.TargetBranch)

	printHeader(cfg)
	mustSetupGitConfig()

	prs := mustFetchQualifiedPRs(cfg)

	fmt.Printf("Preparing target branch '%s' from '%s'...\n", cfg.TargetBranch, cfg.TrunkBranch)
	prepareTargetBranch(cfg)

	if len(prs) == 0 {
		labels := strings.Join(cfg.RequiredLabels, ", ")
		fmt.Printf("\nNo qualifying PRs found for labels [%s].\n", labels)
		fmt.Printf("Pushing '%s' as a clean mirror of '%s'...", cfg.TargetBranch, cfg.TrunkBranch)
		if err := pushChanges(cfg); err != nil {
			log.Fatalf("\npush failed: %v", err)
		}
		fmt.Println(" done.")
		return
	}

	mergedPRs, err := processPRs(prs, cfg.TargetBranch)
	if err != nil {
		log.Fatalf("merge process aborted: %v", err)
	}
	if len(mergedPRs) > 0 {
		updateMergeHistory(mergedPRs)
	}

	fmt.Printf("Pushing '%s' to remote...", cfg.TargetBranch)
	if err := pushChanges(cfg); err != nil {
		log.Fatalf("\npush failed: %v", err)
	}
	fmt.Println(" done.")
}

// printHeader prints a summary of the action configuration
func printHeader(cfg Config) {
	sep := strings.Repeat("=", 50)
	labels := strings.Join(cfg.RequiredLabels, ", ")
	if labels == "" {
		labels = "(none — all open PRs qualify)"
	}
	fmt.Println(sep)
	fmt.Println("  Feature Branching")
	fmt.Printf("  Repo   : %s/%s\n", cfg.Owner, cfg.Repo)
	fmt.Printf("  Trunk  : %s\n", cfg.TrunkBranch)
	fmt.Printf("  Target : %s\n", cfg.TargetBranch)
	fmt.Printf("  Labels : %s\n", labels)
	fmt.Println(sep)
	fmt.Println()
}

// mustParseConfig enforces valid configuration
func mustParseConfig() Config {
	cfg, err := parseConfig()
	if err != nil {
		log.Fatal("invalid configuration:", err)
	}
	return cfg
}

// parseConfig initializes configuration from flags
func parseConfig() (Config, error) {
	var cfg Config
	var labels string

	flag.StringVar(&cfg.GithubToken, "github_token", "", "GitHub access token")
	flag.StringVar(&cfg.Owner, "owner", "", "Repository owner")
	flag.StringVar(&cfg.Repo, "repo", "", "Repository name")
	flag.StringVar(&cfg.TrunkBranch, "trunk_branch", "main", "Base branch name")
	flag.StringVar(&cfg.TargetBranch, "target_branch", "", "Target branch name")
	flag.StringVar(&labels, "labels", "", "Required PR labels (comma separated)")
	flag.StringVar(&cfg.GitHubOutput, "github_output", "", "GitHub outputs file path")
	flag.Parse()

	if cfg.GithubToken == "" {
		return cfg, fmt.Errorf("missing required parameter: 'github_token'")
	}
	if cfg.Owner == "" {
		return cfg, fmt.Errorf("missing required parameter: 'owner'")
	}
	if cfg.Repo == "" {
		return cfg, fmt.Errorf("missing required parameter: 'repo'")
	}
	if cfg.GitHubOutput == "" {
		return cfg, fmt.Errorf("missing required parameter: 'github_output'")
	}

	// Set default target branch if not provided
	if cfg.TargetBranch == "" {
		cfg.TargetBranch = fmt.Sprintf("pre-%s", cfg.TrunkBranch)
	}

	cfg.RequiredLabels = parseLabels(labels)
	return cfg, nil
}

// parseLabels converts comma-separated string to slice
func parseLabels(input string) []string {
	if input == "" {
		return make([]string, 0)
	}
	return strings.Split(input, ",")
}

// mustSetupGitConfig configures Git with safe defaults
func mustSetupGitConfig() {
	if err := setupGitConfig(); err != nil {
		log.Fatal("error configuring Git:", err)
	}
}

// setupGitConfig sets global Git configuration
func setupGitConfig() error {
	workspace := os.Getenv("GITHUB_WORKSPACE")
	if workspace == "" {
		workspace = "/github/workspace"
	}

	// Slice preserves deterministic iteration order
	configs := []struct{ key, value string }{
		{"safe.directory", workspace},
		{"user.name", "github-actions[bot]"},
		{"user.email", "41898282+github-actions[bot]@users.noreply.github.com"},
		{"advice.addIgnoredFile", "false"},
	}

	for _, c := range configs {
		if err := runGitCommand("config", "--global", c.key, c.value); err != nil {
			return fmt.Errorf("git config error: %w", err)
		}
	}
	return nil
}

// mustFetchQualifiedPRs retrieves PRs meeting criteria
func mustFetchQualifiedPRs(cfg Config) []GitHubPR {
	prs, err := fetchQualifiedPRs(cfg)
	if err != nil {
		log.Fatal("error fetching PRs:", err)
	}
	return prs
}

// fetchQualifiedPRs retrieves all open PRs from GitHub API using pagination
func fetchQualifiedPRs(cfg Config) ([]GitHubPR, error) {
	var allPRs []GitHubPR
	page := 1

	for {
		apiURL := fmt.Sprintf("%s/repos/%s/%s/pulls?state=open&base=%s&sort=created&direction=asc&per_page=100&page=%d",
			githubAPI, cfg.Owner, cfg.Repo, cfg.TrunkBranch, page)

		batch, err := fetchPRsPage(cfg, apiURL)
		if err != nil {
			return nil, err
		}

		allPRs = append(allPRs, batch...)

		if len(batch) < 100 {
			break
		}
		page++
	}

	return filterPRs(allPRs, cfg.RequiredLabels), nil
}

// fetchPRsPage retrieves a single page of PRs from the GitHub API
func fetchPRsPage(cfg Config, apiURL string) ([]GitHubPR, error) {
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("request creation failed: %w", err)
	}

	req.Header.Set("Authorization", "token "+cfg.GithubToken)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", userAgent)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request API failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("response API status %d", resp.StatusCode)
	}

	var rawPRs []struct {
		Number    int    `json:"number"`
		Title     string `json:"title"`
		State     string `json:"state"`
		CreatedAt string `json:"created_at"`
		Base      struct {
			Ref string `json:"ref"`
		} `json:"base"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&rawPRs); err != nil {
		return nil, fmt.Errorf("response API decoding failed: %w", err)
	}

	prs := make([]GitHubPR, len(rawPRs))
	for i, raw := range rawPRs {
		labels := make([]string, len(raw.Labels))
		for j, l := range raw.Labels {
			labels[j] = l.Name
		}
		prs[i] = GitHubPR{
			Number:    raw.Number,
			Title:     raw.Title,
			State:     raw.State,
			CreatedAt: raw.CreatedAt,
			Base:      raw.Base,
			Labels:    labels,
		}
	}

	return prs, nil
}

// filterPRs selects PRs with required labels
func filterPRs(prs []GitHubPR, requiredLabels []string) []GitHubPR {
	var filtered []GitHubPR
	for _, pr := range prs {
		if hasAnyLabel(pr.Labels, requiredLabels) {
			filtered = append(filtered, pr)
		}
	}
	return filtered
}

// hasAnyLabel checks for label matches
func hasAnyLabel(prLabels []string, required []string) bool {
	if len(required) == 0 {
		return true
	}

	prLabelSet := make(map[string]struct{})
	for _, l := range prLabels {
		prLabelSet[strings.ToLower(l)] = struct{}{}
	}

	for _, req := range required {
		if _, exists := prLabelSet[strings.ToLower(req)]; exists {
			return true
		}
	}
	return false
}

// prepareTargetBranch resets target branch
func prepareTargetBranch(cfg Config) {
	if err := runGitCommand("checkout", cfg.TrunkBranch); err != nil {
		log.Fatalf("checkout to trunk branch failed: %v", err)
	}

	if branchExists(cfg.TargetBranch) {
		if err := runGitCommand("branch", "-D", cfg.TargetBranch); err != nil {
			log.Fatalf("delete target branch failed: %v", err)
		}
	}

	if err := runGitCommand("checkout", "-B", cfg.TargetBranch); err != nil {
		log.Fatalf("create target branch failed: %v", err)
	}
}

// branchExists checks if a Git branch exists
func branchExists(branch string) bool {
	return runGitCommand("show-ref", "--verify", fmt.Sprintf("refs/heads/%s", branch)) == nil
}

// runGitCommandWithOutput executes a Git command and returns its combined output
func runGitCommandWithOutput(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("'git %s' failed: %s\n%s",
			strings.Join(args, " "), err, string(output))
	}
	return string(output), nil
}

// runGitCommand executes a Git command discarding its output
func runGitCommand(args ...string) error {
	_, err := runGitCommandWithOutput(args...)
	return err
}

// getConflictingFiles returns files with unresolved merge conflicts in the index.
// Uses git ls-files --unmerged which directly queries the index for stages 1/2/3,
// working correctly across all git versions and squash merge scenarios.
func getConflictingFiles() []string {
	// Format per line: "<mode> <sha> <stage>\t<filename>"
	// Conflicted files appear 3 times (stages 1, 2, 3) — deduplicate by filename.
	output, err := exec.Command("git", "ls-files", "--unmerged").Output()
	if err != nil || len(output) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if idx := strings.Index(line, "\t"); idx >= 0 {
			f := strings.TrimSpace(line[idx+1:])
			if f != "" {
				if _, exists := seen[f]; !exists {
					seen[f] = struct{}{}
					files = append(files, f)
				}
			}
		}
	}
	return files
}

// firstLine returns the first non-empty line of a string,
// avoiding multi-line raw git output in user-facing messages.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return s
}

// processPRs handles the PR merging pipeline with progress output.
// Returns an error and aborts immediately if any PR fails to merge,
// preserving the remote target branch in its previous conflict-free state.
func processPRs(prs []GitHubPR, targetBranch string) ([]MergeRecord, error) {
	total := len(prs)
	logPRsToMerge(prs, targetBranch)

	fmt.Printf("Merging into '%s':\n", targetBranch)

	var mergedPRs []MergeRecord
	for i, pr := range prs {
		fmt.Printf("  [%d/%d] #%d \"%s\" ... ", i+1, total, pr.Number, pr.Title)
		if err := processSinglePR(pr); err != nil {
			if errors.Is(err, ErrEmptyMerge) {
				fmt.Println("SKIPPED (changes already in target branch)")
				runGitCommand("reset", "--hard", "HEAD")
				continue
			}
			var conflictErr *ConflictError
			if errors.As(err, &conflictErr) {
				fmt.Println("CONFLICT")
				fmt.Print(strings.TrimRight(conflictErr.GitOutput, "\n"))
				fmt.Println()
			} else {
				fmt.Printf("FAILED\n         Reason: %s\n", firstLine(err.Error()))
			}
			fmt.Printf("\nMerge aborted: PR #%d could not be merged into '%s'.\n", pr.Number, targetBranch)
			fmt.Printf("Target branch '%s' was not updated.\n", targetBranch)
			runGitCommand("reset", "--hard", "HEAD")
			return nil, fmt.Errorf("PR #%d could not be merged: %w", pr.Number, err)
		}
		fmt.Println("OK")
		mergedPRs = append(mergedPRs, createMergeRecord(pr))
	}

	fmt.Printf("\n%d/%d PR(s) merged successfully.\n", len(mergedPRs), total)
	return mergedPRs, nil
}

// logPRsToMerge prints a summary of the PRs queued for merging
func logPRsToMerge(prs []GitHubPR, targetBranch string) {
	fmt.Printf("\nFound %d qualifying PR(s) to merge into '%s':\n", len(prs), targetBranch)
	for i, pr := range prs {
		labels := strings.Join(pr.Labels, ", ")
		fmt.Printf("  [%d/%d] #%d  \"%s\"  [%s]\n", i+1, len(prs), pr.Number, pr.Title, labels)
	}
	fmt.Println()
}

// processSinglePR handles individual PR merging
func processSinglePR(pr GitHubPR) error {
	branch := fmt.Sprintf("pr-%d", pr.Number)

	if err := runGitCommand("fetch", "origin", fmt.Sprintf("pull/%d/head:%s", pr.Number, branch)); err != nil {
		return fmt.Errorf("fetch PR branch '%s' failed: %w", branch, err)
	}

	// Capture merge output separately so it can be shown to the user as-is
	// without being embedded in the error chain.
	mergeOutput, mergeErr := exec.Command("git", "merge", "--squash", branch).CombinedOutput()
	if mergeErr != nil {
		if files := getConflictingFiles(); len(files) > 0 {
			return &ConflictError{Files: files, GitOutput: string(mergeOutput)}
		}
		return fmt.Errorf("squash merge failed: %s", firstLine(string(mergeOutput)))
	}

	if err := runGitCommand("commit", "-m", pr.Title); err != nil {
		if strings.Contains(err.Error(), "nothing to commit") {
			return ErrEmptyMerge
		}
		return fmt.Errorf("create commit failed: %w", err)
	}

	return nil
}

// updateMergeHistory persists merge records
func updateMergeHistory(merges []MergeRecord) {
	if err := updateRefHistory(merges); err != nil {
		log.Fatal("error updating history:", err)
	}
}

// updateRefHistory writes merge history to file
func updateRefHistory(merges []MergeRecord) error {
	history := RefHistory{Merges: merges}
	data, err := json.MarshalIndent(history, "", "  ")
	if err != nil {
		return fmt.Errorf("history serialization failed: %w", err)
	}

	if err := os.WriteFile(refHistoryFile, data, 0644); err != nil {
		return fmt.Errorf("file write failed: %w", err)
	}

	if err := runGitCommand("add", refHistoryFile); err != nil {
		return fmt.Errorf("staging history file failed: %w", err)
	}
	return runGitCommand("commit", "-m", "chore: update ref-history")
}

// pushChanges pushes to remote repository
func pushChanges(cfg Config) error {
	return runGitCommand("push", "origin", cfg.TargetBranch, "--force")
}

// createMergeRecord generates merge metadata
func createMergeRecord(pr GitHubPR) MergeRecord {
	output, err := runGitCommandWithOutput("rev-parse", "HEAD")
	commit := strings.TrimSpace(output)
	if err != nil {
		commit = "unknown"
	}
	return MergeRecord{
		PR:        pr.Number,
		Commit:    commit,
		Timestamp: time.Now().UTC(),
	}
}

// setOutput writes a key=value pair to the GitHub Actions output file.
// Errors are logged as warnings since output failure should not abort the action.
func setOutput(cfg Config, name, value string) {
	f, err := os.OpenFile(cfg.GitHubOutput, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("warning: failed to open output file: %v", err)
		return
	}
	defer f.Close()

	if _, err := fmt.Fprintf(f, "%s=%s\n", name, value); err != nil {
		log.Printf("warning: failed to write output '%s': %v", name, err)
	}
}
