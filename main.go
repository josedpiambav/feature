package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/go-github/v56/github"
	"golang.org/x/oauth2"
)

// Config holds all application configuration parameters
type Config struct {
	GithubToken    string   // GitHub access token with repo permissions
	Owner          string   // Repository owner (user or organization)
	Repo           string   // Repository name
	TrunkBranch    string   // Base branch (typically main/master)
	TargetBranch   string   // Destination branch for merges
	MergeStrategy  string   // Merge strategy: merge, squash, or rebase
	RequiredLabels []string // Required PR labels to trigger merge
	RefHistoryFile string   // Path to reference history file
}

// MergeRecord represents a single merge operation record
type MergeRecord struct {
	PR        int       `json:"pr"`        // Pull Request number
	Commit    string    `json:"commit"`    // Resulting commit SHA
	Target    string    `json:"target"`    // Target branch name
	Timestamp time.Time `json:"timestamp"` // Merge timestamp
}

// RefHistory maintains a log of all merge operations
type RefHistory struct {
	Merges []MergeRecord `json:"merges"` // List of merge records
}

func main() {
	cfg := parseConfig()
	log.Printf("cfg: %+v\n", cfg)

	client := createGitHubClient(cfg)
	log.Printf("client: %+v\n", client)

	// Ensure target branch exists and is up to date
	if err := createOrUpdateTargetBranch(client, cfg); err != nil {
		log.Fatalf("Failed to prepare target branch: %v", err)
	}

	// Process all eligible PRs
	prs, err := getQualifiedPullRequests(client, cfg)
	if err != nil {
		log.Fatalf("Error fetching PRs: %v", err)
	}

	for _, pr := range prs {
		handlePullRequest(client, cfg, pr)
	}
}

// parseConfig initializes configuration from command line flags
func parseConfig() Config {
	var cfg Config

	flag.StringVar(&cfg.GithubToken, "github_token", "",
		"GitHub access token with repo permissions")
	flag.StringVar(&cfg.Owner, "owner", "",
		"Repository owner (user/organization)")
	flag.StringVar(&cfg.Repo, "repo", "",
		"Repository name")
	flag.StringVar(&cfg.TrunkBranch, "trunk_branch", "main",
		"Base branch to merge from")
	flag.StringVar(&cfg.TargetBranch, "target_branch", "",
		"Destination branch for merges (default: pre-{trunk})")
	flag.StringVar(&cfg.MergeStrategy, "merge_strategy", "merge",
		"Merge strategy: merge, squash, rebase")
	flag.StringVar(&cfg.RefHistoryFile, "ref_history_file",
		".github/ref-history.json", "Path to merge history file")

	var labels string
	flag.StringVar(&labels, "labels", "",
		"Comma-separated list of required PR labels")
	flag.Parse()

	// Set default target branch if not provided
	if cfg.TargetBranch == "" {
		cfg.TargetBranch = fmt.Sprintf("pre-%s", cfg.TrunkBranch)
	}

	// Split comma-separated labels
	cfg.RequiredLabels = strings.Split(labels, ",")

	return cfg
}

// createGitHubClient initializes authenticated GitHub client
func createGitHubClient(cfg Config) *github.Client {
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: cfg.GithubToken},
	)
	return github.NewClient(oauth2.NewClient(context.Background(), ts))
}

// createOrUpdateTargetBranch ensures target branch exists and is based on trunk
func createOrUpdateTargetBranch(client *github.Client, cfg Config) error {
	// Check if target branch exists
	_, resp, err := client.Repositories.GetBranch(
		context.Background(),
		cfg.Owner,
		cfg.Repo,
		cfg.TargetBranch,
		0,
	)

	// Branch exists, no action needed
	if err == nil {
		return nil
	}

	// Unexpected error (not 404 Not Found)
	if resp.StatusCode != 404 {
		return fmt.Errorf("branch check failed: %w", err)
	}

	// Get latest commit from trunk branch
	trunk, _, err := client.Repositories.GetBranch(
		context.Background(),
		cfg.Owner,
		cfg.Repo,
		cfg.TrunkBranch,
		0,
	)
	if err != nil {
		return fmt.Errorf("failed to get trunk branch: %w", err)
	}

	// Create new branch reference
	ref := fmt.Sprintf("refs/heads/%s", cfg.TargetBranch)
	_, _, err = client.Git.CreateRef(
		context.Background(),
		cfg.Owner,
		cfg.Repo,
		&github.Reference{
			Ref:    &ref,
			Object: &github.GitObject{SHA: trunk.Commit.SHA},
		},
	)

	return err
}

// getQualifiedPullRequests fetches open PRs with required labels
func getQualifiedPullRequests(client *github.Client, cfg Config) ([]*github.PullRequest, error) {
	// List all open PRs targeting the trunk branch
	prs, _, err := client.PullRequests.List(
		context.Background(),
		cfg.Owner,
		cfg.Repo,
		&github.PullRequestListOptions{
			State: "open",
			Base:  cfg.TrunkBranch,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list PRs: %w", err)
	}

	// Filter PRs with required labels
	var qualifiedPRs []*github.PullRequest
	for _, pr := range prs {
		if hasRequiredLabels(pr.Labels, cfg.RequiredLabels) {
			qualifiedPRs = append(qualifiedPRs, pr)
		}
	}

	return qualifiedPRs, nil
}

// hasRequiredLabels verifies if PR contains all required labels
func hasRequiredLabels(prLabels []*github.Label, required []string) bool {
	// Create lowercase set of PR labels for case-insensitive comparison
	presentLabels := make(map[string]struct{})
	for _, l := range prLabels {
		presentLabels[strings.ToLower(l.GetName())] = struct{}{}
	}

	// Check all required labels exist in PR
	for _, reqLabel := range required {
		reqLabel = strings.TrimSpace(reqLabel)
		if reqLabel == "" {
			continue // Skip empty labels
		}
		if _, exists := presentLabels[strings.ToLower(reqLabel)]; !exists {
			return false
		}
	}
	return true
}

// handlePullRequest processes a single PR merge operation
func handlePullRequest(client *github.Client, cfg Config, pr *github.PullRequest) {
	log.Printf("Processing PR #%d", pr.GetNumber())

	// Verify merge readiness
	if pr.GetMergeableState() != "clean" {
		log.Printf("PR #%d not mergeable: %s", pr.GetNumber(), pr.GetMergeableState())
		return
	}

	// Perform the merge operation
	commitSHA, err := executeMerge(client, cfg, pr)
	if err != nil {
		log.Printf("Merge failed for PR #%d: %v", pr.GetNumber(), err)
		return
	}

	// Update merge history
	if err := updateMergeHistory(client, cfg, pr.GetNumber(), commitSHA); err != nil {
		log.Printf("History update failed: %v", err)
	}

	// Cleanup feature branch if needed
	if cfg.MergeStrategy == "squash" || cfg.MergeStrategy == "rebase" {
		if err := deleteFeatureBranch(client, cfg, pr.GetHead().GetRef()); err != nil {
			log.Printf("Branch cleanup failed: %v", err)
		}
	}
}

// deleteFeatureBranch removes the feature branch after merge
func deleteFeatureBranch(client *github.Client, cfg Config, branchName string) error {
	// GitHub API requires refs in 'heads/' format
	ref := fmt.Sprintf("heads/%s", branchName)
	_, err := client.Git.DeleteRef(
		context.Background(),
		cfg.Owner,
		cfg.Repo,
		ref,
	)
	if err != nil {
		return fmt.Errorf("failed to delete branch %s: %w", branchName, err)
	}
	return nil
}

// executeMerge performs the actual merge operation using specified strategy
func executeMerge(client *github.Client, cfg Config, pr *github.PullRequest) (string, error) {
	commitMsg := fmt.Sprintf("Merge PR #%d into %s (%s)",
		pr.GetNumber(), cfg.TargetBranch, cfg.MergeStrategy)

	mergeOptions := &github.PullRequestOptions{
		MergeMethod: cfg.MergeStrategy,
		CommitTitle: fmt.Sprintf("Merge PR #%d", pr.GetNumber()),
	}

	result, resp, err := client.PullRequests.Merge(
		context.Background(),
		cfg.Owner,
		cfg.Repo,
		pr.GetNumber(),
		commitMsg,
		mergeOptions,
	)

	if err != nil {
		return "", fmt.Errorf("merge API error: %w (status: %d)", err, resp.StatusCode)
	}

	return result.GetSHA(), nil
}

// updateMergeHistory maintains the reference history file
func updateMergeHistory(client *github.Client, cfg Config, prNumber int, commitSHA string) error {
	// Get current history content
	content, sha, err := getHistoryContent(client, cfg)
	if err != nil && !isNotFoundError(err) {
		return err
	}

	var history RefHistory
	if len(content) > 0 {
		if err := json.Unmarshal(content, &history); err != nil {
			return fmt.Errorf("history parse error: %w", err)
		}
	}

	// Add new merge record
	history.Merges = append(history.Merges, MergeRecord{
		PR:        prNumber,
		Commit:    commitSHA,
		Target:    cfg.TargetBranch,
		Timestamp: time.Now().UTC(),
	})

	// Serialize updated history
	newContent, err := json.MarshalIndent(history, "", "  ")
	if err != nil {
		return fmt.Errorf("history serialization error: %w", err)
	}

	// Commit changes to repository
	return commitHistoryUpdate(client, cfg, newContent, sha)
}

// getHistoryContent retrieves current ref-history file contents
func getHistoryContent(client *github.Client, cfg Config) ([]byte, string, error) {
	// Get file contents with explicit branch reference
	file, _, _, err := client.Repositories.GetContents(
		context.Background(),
		cfg.Owner,
		cfg.Repo,
		cfg.RefHistoryFile,
		&github.RepositoryContentGetOptions{
			Ref: cfg.TargetBranch,
		},
	)

	// Handle 404 (file not found)
	if isNotFoundError(err) {
		return []byte{}, "", nil
	}

	if err != nil {
		return nil, "", fmt.Errorf("failed to get history file: %w", err)
	}

	// Decode file content from base64
	content, err := file.GetContent()
	if err != nil {
		return nil, "", fmt.Errorf("content decoding failed: %w", err)
	}

	return []byte(content), file.GetSHA(), nil
}

// isNotFoundError checks if error is a 404 response
func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "404") ||
		strings.Contains(err.Error(), "Not Found")
}

// commitHistoryUpdate writes updated history to repository
func commitHistoryUpdate(client *github.Client, cfg Config, content []byte, sha string) error {
	commitMessage := fmt.Sprintf("Update merge history - %s", time.Now().UTC().Format(time.RFC3339))

	// GitHub API requires base64 content
	encodedContent := []byte(content)

	_, _, err := client.Repositories.CreateFile(
		context.Background(),
		cfg.Owner,
		cfg.Repo,
		cfg.RefHistoryFile,
		&github.RepositoryContentFileOptions{
			Message: github.String(commitMessage),
			Content: encodedContent,
			SHA:     github.String(sha),
			Branch:  github.String(cfg.TargetBranch),
			Committer: &github.CommitAuthor{
				Name:  github.String("Feature Branch Bot"),
				Email: github.String("bot@example.com"),
			},
		},
	)

	if err != nil {
		return fmt.Errorf("failed to commit history: %w", err)
	}
	return nil
}
