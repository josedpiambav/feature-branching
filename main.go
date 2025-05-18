package main

import (
	"encoding/json"
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

	mustSetupGitConfig()
	prs := mustFetchQualifiedPRs(cfg)
	prepareTargetBranch(cfg)

	mergedPRs := processPRs(prs)
	updateMergeHistory(mergedPRs)
	pushChanges(cfg)
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

	// Validate required parameters
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
	configs := map[string]string{
		"safe.directory":        "/github/workspace",
		"user.name":             "github-actions[bot]",
		"user.email":            "41898282+github-actions[bot]@users.noreply.github.com",
		"advice.addIgnoredFile": "false",
	}

	for key, value := range configs {
		if err := runGitCommand("config", "--global", key, value); err != nil {
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

// fetchQualifiedPRs retrieves open PRs from GitHub API
func fetchQualifiedPRs(cfg Config) ([]GitHubPR, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls?state=open&base=%s&sort=created&direction=asc",
		githubAPI, cfg.Owner, cfg.Repo, cfg.TrunkBranch)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("request creation failed: %w", err)
	}

	// Set request headers
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

	// Convert raw PRs to simplified structure
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

	return filterPRs(prs, cfg.RequiredLabels), nil
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

// runGitCommand executes Git commands with unified error handling
func runGitCommand(args ...string) error {
	cmd := exec.Command("git", args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("'git %s' failed: %s\n%s",
			strings.Join(args, " "), err, string(output))
	}
	return nil
}

// processPRs handles PR merging pipeline
func processPRs(prs []GitHubPR) []MergeRecord {
	var mergedPRs []MergeRecord
	for _, pr := range prs {
		if err := processSinglePR(pr); err != nil {
			log.Printf("PR #%d failed: %v", pr.Number, err)
			continue
		}
		mergedPRs = append(mergedPRs, createMergeRecord(pr))
	}
	return mergedPRs
}

// processSinglePR handles individual PR merging
func processSinglePR(pr GitHubPR) error {
	branch := fmt.Sprintf("pr-%d", pr.Number)

	// Execute PR processing steps
	if err := runGitCommand("fetch", "origin", fmt.Sprintf("pull/%d/head:%s", pr.Number, branch)); err != nil {
		return fmt.Errorf("fetch PR branch '%s' failed: %w", branch, err)
	}

	if err := runGitCommand("merge", "--squash", branch); err != nil {
		return fmt.Errorf("squash merge failed: %w", err)
	}

	if err := runGitCommand("commit", "-m", pr.Title); err != nil {
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
		return err
	}
	return runGitCommand("commit", "-m", "chore: update ref-history")
}

// pushChanges pushes to remote repository
func pushChanges(cfg Config) error {
	return runGitCommand("push", "origin", cfg.TargetBranch, "--force")
}

// createMergeRecord generates merge metadata
func createMergeRecord(pr GitHubPR) MergeRecord {
	return MergeRecord{
		PR:        pr.Number,
		Commit:    getLatestCommitSHA(),
		Timestamp: time.Now().UTC(),
	}
}

// getLatestCommitSHA retrieves current HEAD SHA
func getLatestCommitSHA() string {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(output))
}

// setOutput assigns a return value
func setOutput(cfg Config, name, value string) error {
	f, err := os.OpenFile(cfg.GitHubOutput, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open output file failed: %w", err)
	}
	defer f.Close()

	if _, err := fmt.Fprintf(f, "%s=%s\n", name, value); err != nil {
		return fmt.Errorf("write output failed: %w", err)
	}
	return nil
}
