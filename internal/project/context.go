// Package project discovers and generates project context for AI-powered code reviews.
//
// It uses a layered approach:
//  1. Discover explicit documentation (README, /docs, AI agent config files, ARCHITECTURE.md, etc.)
//  2. Read manifest files (go.mod, package.json, Cargo.toml, etc.) for tech stack info
//  3. Capture directory structure (top-level + 1-2 levels) for architecture hints
//  4. If docs are thin, use a cheap LLM to infer project purpose/domain from the gathered signals
//
// The result is a compact project briefing injected into batch review prompts so each
// batch doesn't waste tokens re-discovering what the project is about.
package project

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/andreujuanc/prr/internal/ai"
)

// maxDocBytes is the maximum bytes to read from any single documentation file.
const maxDocBytes = 8000

// maxTotalDocBytes caps the total documentation content sent to the LLM.
const maxTotalDocBytes = 16000

// maxDirDepth is how deep the directory tree scan goes.
const maxDirDepth = 2

// maxInferenceOutput caps the LLM inference output.
const maxInferenceOutput = 400

// Context holds the discovered project context.
type Context struct {
	// Summary is the final project briefing, ready for injection into prompts.
	Summary string

	// InputHash is a SHA-256 hash of all inputs used to generate the summary.
	// Used for cache invalidation.
	InputHash string

	// FromCache indicates whether this result was loaded from cache.
	FromCache bool
}

// discoveredInputs holds all raw material gathered from the repo.
type discoveredInputs struct {
	docs      map[string]string // filename -> content
	manifests map[string]string // filename -> content
	dirTree   string            // formatted directory tree
}

// docPatterns are documentation files to look for at the repo root.
var docPatterns = []string{
	"README.md",
	"README",
	"README.txt",
	"ARCHITECTURE.md",
	"CONTRIBUTING.md",
	"DESIGN.md",
	"OVERVIEW.md",
}

// docDirs are directories to scan for documentation files.
var docDirs = []string{
	"docs",
	"doc",
	"documentation",
}

// aiConfigFiles are AI agent configuration files that often contain project context.
var aiConfigFiles = []string{
	".cursor/rules",
	".cursorrules",
	".github/copilot-instructions.md",
	"CLAUDE.md",
	".claude/settings.json",
	"AGENTS.md",
	".windsurfrules",
	"codex.md",
	".codex/instructions.md",
	".ai/context.md",
	".prr/instructions.md",
	".github/prr-instructions.md",
}

// manifestFiles are package/dependency files that reveal the tech stack.
var manifestFiles = []string{
	"go.mod",
	"go.sum",
	"package.json",
	"Cargo.toml",
	"pyproject.toml",
	"requirements.txt",
	"Pipfile",
	"Gemfile",
	"pom.xml",
	"build.gradle",
	"build.gradle.kts",
	"composer.json",
	"mix.exs",
	"pubspec.yaml",
	"CMakeLists.txt",
	"Makefile",
	"Dockerfile",
	"docker-compose.yml",
	"docker-compose.yaml",
}

// Discover gathers project context from a repository root.
// If an LLM client is provided and documentation is thin, it uses the LLM
// to infer project purpose. The client can be nil to skip LLM inference.
// If cachedHash is non-empty and matches the current inputs, Discover returns
// early with FromCache=true and an empty Summary (caller should use cached version).
func Discover(ctx context.Context, repoRoot string, client ai.Client, cachedHash string, onProgress func(string)) (*Context, error) {
	if onProgress == nil {
		onProgress = func(string) {}
	}

	onProgress("Discovering project context...")

	inputs, err := gatherInputs(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("gathering project inputs: %w", err)
	}

	inputHash := hashInputs(inputs)

	// If cached hash matches, inputs haven't changed — caller should use cached version
	if cachedHash != "" && cachedHash == inputHash {
		onProgress("Project context unchanged (cache hit)")
		return &Context{
			InputHash: inputHash,
			FromCache: true,
		}, nil
	}

	// Determine if we have enough documentation to skip LLM inference
	totalDocSize := 0
	for _, content := range inputs.docs {
		totalDocSize += len(content)
	}

	var summary string

	if totalDocSize >= 200 && client != nil {
		// We have docs — use LLM to produce a concise summary
		onProgress("Summarizing project context...")
		summary, err = summarizeWithLLM(ctx, client, inputs)
		if err != nil {
			// Non-fatal — fall back to raw synthesis
			log.Printf("Project context summarization failed: %v", err)
			summary = synthesizeFromDocs(inputs)
		} else {
			log.Printf("Project context: summarized from %d doc files (%d bytes), %d manifests",
				len(inputs.docs), totalDocSize, len(inputs.manifests))
		}
	} else if totalDocSize >= 200 {
		// Docs available but no LLM client — raw synthesis
		onProgress("Building project context from documentation...")
		summary = synthesizeFromDocs(inputs)
		log.Printf("Project context: built from %d doc files (%d bytes), %d manifests",
			len(inputs.docs), totalDocSize, len(inputs.manifests))
	} else if client != nil {
		// Docs are thin — use LLM to infer
		onProgress("Inferring project context via LLM...")
		summary, err = inferWithLLM(ctx, client, inputs)
		if err != nil {
			// Non-fatal — fall back to whatever we have
			log.Printf("Project context LLM inference failed: %v", err)
			summary = synthesizeFromDocs(inputs)
		} else {
			log.Printf("Project context: inferred via LLM from %d manifests + dir tree",
				len(inputs.manifests))
		}
	} else {
		// No docs, no LLM — best effort from manifests + tree
		summary = synthesizeFromDocs(inputs)
		log.Printf("Project context: minimal (no docs, no LLM), %d manifests", len(inputs.manifests))
	}

	onProgress("Project context ready")

	return &Context{
		Summary:   summary,
		InputHash: inputHash,
	}, nil
}

// gatherInputs collects all raw material from the repository.
func gatherInputs(repoRoot string) (*discoveredInputs, error) {
	inputs := &discoveredInputs{
		docs:      make(map[string]string),
		manifests: make(map[string]string),
	}

	// 1. Discover documentation files at repo root
	for _, pattern := range docPatterns {
		path := filepath.Join(repoRoot, pattern)
		if content, err := readFileCapped(path, maxDocBytes); err == nil {
			inputs.docs[pattern] = content
		}
	}

	// 2. Discover AI agent config files
	for _, pattern := range aiConfigFiles {
		path := filepath.Join(repoRoot, pattern)
		if content, err := readFileCapped(path, maxDocBytes); err == nil {
			inputs.docs["[AI Config] "+pattern] = content
		}
	}

	// 3. Scan doc directories for .md files
	for _, dir := range docDirs {
		dirPath := filepath.Join(repoRoot, dir)
		if info, err := os.Stat(dirPath); err == nil && info.IsDir() {
			_ = filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					log.Printf("Warning: error walking %s: %v", path, err)
					return nil
				}
				if info.IsDir() {
					return nil
				}
				ext := strings.ToLower(filepath.Ext(path))
				if ext == ".md" || ext == ".txt" || ext == ".rst" {
					relPath, _ := filepath.Rel(repoRoot, path)
					if content, readErr := readFileCapped(path, maxDocBytes); readErr == nil {
						inputs.docs[relPath] = content
					}
				}
				return nil
			})
		}
	}

	// 4. Read manifest files
	for _, pattern := range manifestFiles {
		path := filepath.Join(repoRoot, pattern)
		// Manifests are usually small, but cap at 4KB
		if content, err := readFileCapped(path, 4000); err == nil {
			inputs.manifests[pattern] = content
		}
	}

	// 5. Capture directory tree
	inputs.dirTree = buildDirTree(repoRoot, maxDirDepth)

	return inputs, nil
}

// synthesizeFromDocs builds a project context string from discovered materials
// without using an LLM. This is a structured concatenation.
func synthesizeFromDocs(inputs *discoveredInputs) string {
	var b strings.Builder

	b.WriteString("## Project Context\n\n")

	// Documentation
	if len(inputs.docs) > 0 {
		b.WriteString("### Documentation\n\n")
		totalBytes := 0
		// Sort keys for deterministic output
		keys := sortedKeys(inputs.docs)
		for _, name := range keys {
			content := inputs.docs[name]
			if totalBytes+len(content) > maxTotalDocBytes {
				remaining := maxTotalDocBytes - totalBytes
				if remaining > 200 {
					content = content[:remaining] + "\n[...truncated]"
				} else {
					continue
				}
			}
			b.WriteString(fmt.Sprintf("#### %s\n\n%s\n\n", name, strings.TrimSpace(content)))
			totalBytes += len(content)
		}
	}

	// Tech stack from manifests
	if len(inputs.manifests) > 0 {
		b.WriteString("### Tech Stack (from manifests)\n\n")
		keys := sortedKeys(inputs.manifests)
		for _, name := range keys {
			content := inputs.manifests[name]
			b.WriteString(fmt.Sprintf("#### %s\n\n```\n%s\n```\n\n", name, strings.TrimSpace(content)))
		}
	}

	// Directory structure
	if inputs.dirTree != "" {
		b.WriteString("### Repository Structure\n\n```\n")
		b.WriteString(inputs.dirTree)
		b.WriteString("\n```\n\n")
	}

	return b.String()
}

// summarizeWithLLM uses a cheap LLM to compress rich documentation into a concise
// project briefing. Used when docs are plentiful but we need to keep the context
// compact for injection into review prompts.
func summarizeWithLLM(ctx context.Context, client ai.Client, inputs *discoveredInputs) (string, error) {
	var prompt strings.Builder

	prompt.WriteString("Summarize this project into a concise briefing (MAX 200 words, ~800 tokens). ")
	prompt.WriteString("Cover these points in a dense paragraph — no bullet points, no headers:\n")
	prompt.WriteString("1. What it IS (type of software)\n")
	prompt.WriteString("2. What DOMAIN it serves\n")
	prompt.WriteString("3. Key technologies and frameworks\n")
	prompt.WriteString("4. Architecture style\n")
	prompt.WriteString("5. Key components/modules\n\n")
	prompt.WriteString("Be factual and dense. Every sentence should convey information. ")
	prompt.WriteString("Do NOT include setup instructions, contribution guidelines, or license info. ")
	prompt.WriteString("Do NOT pad with filler phrases like \"This project is a comprehensive...\" — just state facts.\n\n")

	// Add docs
	if len(inputs.docs) > 0 {
		totalBytes := 0
		keys := sortedKeys(inputs.docs)
		for _, name := range keys {
			content := inputs.docs[name]
			if totalBytes+len(content) > maxTotalDocBytes {
				remaining := maxTotalDocBytes - totalBytes
				if remaining > 200 {
					content = content[:remaining]
				} else {
					continue
				}
			}
			prompt.WriteString(fmt.Sprintf("=== %s ===\n%s\n\n", name, strings.TrimSpace(content)))
			totalBytes += len(content)
		}
	}

	// Add manifests
	if len(inputs.manifests) > 0 {
		for name, content := range inputs.manifests {
			prompt.WriteString(fmt.Sprintf("=== %s ===\n%s\n\n", name, strings.TrimSpace(content)))
		}
	}

	// Add dir tree
	if inputs.dirTree != "" {
		prompt.WriteString("=== Directory Structure ===\n")
		prompt.WriteString(inputs.dirTree)
		prompt.WriteString("\n\n")
	}

	systemPrompt := "You are a technical writer producing ultra-concise project briefings. " +
		"Your output will be injected into LLM prompts as context, so every token matters. " +
		"Be maximally dense and factual. No filler, no fluff, no markdown formatting. " +
		"Output ONLY the briefing paragraph — nothing else."

	messages := []ai.Message{
		{Role: "user", Content: prompt.String()},
	}

	result, err := client.ChatStream(ctx, systemPrompt, messages, nil)
	if err != nil {
		return "", fmt.Errorf("LLM summarization: %w", err)
	}

	return fmt.Sprintf("## Project Context\n\n%s\n", strings.TrimSpace(result)), nil
}

// inferWithLLM uses a cheap LLM to generate a project briefing from thin inputs.
func inferWithLLM(ctx context.Context, client ai.Client, inputs *discoveredInputs) (string, error) {
	// Build the prompt with all available signals
	var prompt strings.Builder

	prompt.WriteString("Based on the following information about a software project, ")
	prompt.WriteString("write a concise project briefing (MAX 200 words, ~800 tokens) that covers:\n")
	prompt.WriteString("1. What the project IS (CLI tool, web API, frontend app, library, database, etc.)\n")
	prompt.WriteString("2. What DOMAIN it serves (fintech, healthcare, developer tools, social media, etc.)\n")
	prompt.WriteString("3. Key TECHNOLOGIES and frameworks used\n")
	prompt.WriteString("4. ARCHITECTURE style (monolith, microservices, serverless, etc.)\n")
	prompt.WriteString("5. Target USERS/consumers of this software\n\n")
	prompt.WriteString("Be factual and dense. Every sentence should convey information. ")
	prompt.WriteString("No bullet points, no headers, no filler phrases. ")
	prompt.WriteString("Say \"unclear\" for anything you can't determine.\n\n")

	// Add any docs we have (even if thin)
	if len(inputs.docs) > 0 {
		prompt.WriteString("### Available Documentation\n\n")
		for name, content := range inputs.docs {
			prompt.WriteString(fmt.Sprintf("#### %s\n%s\n\n", name, content))
		}
	}

	// Add manifests
	if len(inputs.manifests) > 0 {
		prompt.WriteString("### Manifest Files\n\n")
		for name, content := range inputs.manifests {
			prompt.WriteString(fmt.Sprintf("#### %s\n```\n%s\n```\n\n", name, content))
		}
	}

	// Add directory tree
	if inputs.dirTree != "" {
		prompt.WriteString("### Directory Structure\n```\n")
		prompt.WriteString(inputs.dirTree)
		prompt.WriteString("\n```\n\n")
	}

	systemPrompt := "You are a senior software engineer analyzing a project repository. " +
		"Your job is to produce a concise project briefing that will help code reviewers " +
		"understand the project context when reviewing pull requests. " +
		"Be precise and factual. Do not speculate beyond what the evidence supports."

	messages := []ai.Message{
		{Role: "user", Content: prompt.String()},
	}

	result, err := client.ChatStream(ctx, systemPrompt, messages, nil)
	if err != nil {
		return "", fmt.Errorf("LLM inference: %w", err)
	}

	// Wrap in project context header
	return fmt.Sprintf("## Project Context (Auto-Generated)\n\n%s\n", strings.TrimSpace(result)), nil
}

// hashInputs produces a deterministic hash of all discovered inputs.
func hashInputs(inputs *discoveredInputs) string {
	h := sha256.New()

	// Hash docs in sorted order
	for _, k := range sortedKeys(inputs.docs) {
		h.Write([]byte(k))
		h.Write([]byte(inputs.docs[k]))
	}

	// Hash manifests in sorted order
	for _, k := range sortedKeys(inputs.manifests) {
		h.Write([]byte(k))
		h.Write([]byte(inputs.manifests[k]))
	}

	// Hash dir tree
	h.Write([]byte(inputs.dirTree))

	return fmt.Sprintf("%x", h.Sum(nil))
}

// readFileCapped reads a file up to maxBytes, returning its content.
// Uses io.LimitReader to avoid loading entire large files into memory.
func readFileCapped(path string, maxBytes int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, int64(maxBytes)))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// buildDirTree creates a formatted directory tree string.
func buildDirTree(root string, maxDepth int) string {
	var b strings.Builder
	buildDirTreeRecursive(&b, root, "", 0, maxDepth)
	return b.String()
}

// skipDirs are directories to exclude from the tree.
var skipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
	".next":        true,
	"__pycache__":  true,
	".venv":        true,
	"venv":         true,
	"dist":         true,
	"build":        true,
	"target":       true,
	".idea":        true,
	".vscode":      true,
}

// maxEntriesPerDir caps the number of entries processed per directory to avoid
// excessive memory usage in directories with very large numbers of files.
const maxEntriesPerDir = 500

func buildDirTreeRecursive(b *strings.Builder, dir, prefix string, depth, maxDepth int) {
	if depth > maxDepth {
		return
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	// Filter and sort: directories first, then files
	var dirs, files []os.DirEntry
	totalKept := 0
	for _, e := range entries {
		if totalKept >= maxEntriesPerDir {
			b.WriteString(prefix + fmt.Sprintf("... (%d more entries)\n", len(entries)-totalKept))
			break
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") && depth == 0 && !e.IsDir() {
			continue // skip hidden files at root (but allow hidden dirs like .github)
		}
		if e.IsDir() {
			if skipDirs[name] {
				continue
			}
			dirs = append(dirs, e)
		} else {
			files = append(files, e)
		}
		totalKept++
	}

	// Print directories
	for _, d := range dirs {
		b.WriteString(prefix + d.Name() + "/\n")
		buildDirTreeRecursive(b, filepath.Join(dir, d.Name()), prefix+"  ", depth+1, maxDepth)
	}

	// Print files (only at depth 0 and 1 to keep it compact)
	if depth <= 1 {
		for _, f := range files {
			b.WriteString(prefix + f.Name() + "\n")
		}
	} else if len(files) > 0 {
		b.WriteString(fmt.Sprintf("%s(%d files)\n", prefix, len(files)))
	}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
