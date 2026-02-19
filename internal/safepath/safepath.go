package safepath

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResolveNoSymlinkPath resolves a target path, rejects symlink components,
// and ensures the result stays under the provided root path.
func ResolveNoSymlinkPath(root, target string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("safety root is required")
	}
	if strings.TrimSpace(target) == "" {
		return "", fmt.Errorf("target path is required")
	}

	rootAbs, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", fmt.Errorf("resolve root path %s: %w", root, err)
	}
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", fmt.Errorf("resolve root symlinks %s: %w", root, err)
	}

	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("resolve target path %s: %w", target, err)
	}
	targetAbs = filepath.Clean(targetAbs)

	resolvedTarget := targetAbs
	if _, err := os.Stat(targetAbs); err == nil {
		resolved, err := filepath.EvalSymlinks(targetAbs)
		if err != nil {
			return "", fmt.Errorf("resolve target path %s: %w", target, err)
		}
		resolvedTarget = resolved
	} else if errors.Is(err, os.ErrNotExist) {
		parent := filepath.Dir(targetAbs)
		if parent == targetAbs {
			return "", fmt.Errorf("target path has no parent: %s", target)
		}
		resolvedParent, err := filepath.EvalSymlinks(parent)
		if err != nil {
			return "", fmt.Errorf("resolve target parent %s: %w", parent, err)
		}
		resolvedTarget = filepath.Join(resolvedParent, filepath.Base(targetAbs))
	} else {
		return "", err
	}

	if err := ensureNoSymlinkComponents(resolvedTarget); err != nil {
		return "", err
	}

	resolvedRoot := filepath.Clean(rootReal)
	resolvedTarget = filepath.Clean(resolvedTarget)
	rel, err := filepath.Rel(resolvedRoot, resolvedTarget)
	if err != nil {
		return "", fmt.Errorf("resolve relative path for %s: %w", target, err)
	}
	if rel == "." || rel == ".." || rel == "" || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path is outside safety root: %s", target)
	}

	return resolvedTarget, nil
}

func ensureNoSymlinkComponents(candidate string) error {
	current := filepath.Clean(candidate)
	for {
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("path contains symlink component: %s", current)
			}
		} else if !os.IsNotExist(err) {
			return err
		}

		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return nil
}
