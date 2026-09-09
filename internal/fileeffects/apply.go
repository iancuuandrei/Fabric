package fileeffects

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"

	"harness.local/engorch/internal/safepath"
	"harness.local/engorch/internal/worktree"
)

// Apply performs the approved ordered writes. Preconditions: caller holds the
// writer lease and has durably recorded exact authorization and intent. Errors
// may follow partial writes; the caller must observe and persist an outcome.
func Apply(ctx context.Context, b worktree.Binding, p Proposal) error { return apply(ctx, b, p, nil) }

// Preflight establishes current candidate and target accessibility before intent
// persistence. It rejects directory targets and aliases across the whole proposal.
func Preflight(ctx context.Context, b worktree.Binding, p Proposal) error {
	if err := p.AdmitHost(); err != nil {
		return err
	}
	current, err := worktree.Fingerprint(ctx, b)
	if err != nil {
		return err
	}
	if current != p.Before {
		return errors.New("candidate changed before file admission")
	}
	r, err := os.OpenRoot(b.Request.Path)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, c := range p.Changes {
		h, _, _, exists, err := safepath.ReadRegular(r, c.Path, 64<<20)
		if err != nil {
			return err
		}
		if c.BeforeHash == nil && exists || c.BeforeHash != nil && (!exists || h != *c.BeforeHash) {
			return errors.New("file admission precondition changed")
		}
	}
	return nil
}

func apply(ctx context.Context, b worktree.Binding, p Proposal, beforeChange func(int) error) error {
	if err := p.AdmitHost(); err != nil {
		return err
	}
	current, err := worktree.Fingerprint(ctx, b)
	if err != nil {
		return err
	}
	if current != p.Before {
		return errors.New("candidate changed before file effects")
	}
	return applySelected(ctx, b, p, nil, beforeChange)
}

func applySelected(ctx context.Context, b worktree.Binding, p Proposal, selected map[int]bool, beforeChange func(int) error) error {
	root, err := os.OpenRoot(b.Request.Path)
	if err != nil {
		return err
	}
	defer root.Close()
	id, err := p.ID()
	if err != nil {
		return err
	}
	for i, c := range p.Changes {
		if selected != nil && !selected[i] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if beforeChange != nil {
			if err := beforeChange(i); err != nil {
				return err
			}
		}
		h, _, _, exists, err := safepath.ReadRegular(root, c.Path, 64<<20)
		if err != nil {
			return err
		}
		if c.BeforeHash == nil && exists || c.BeforeHash != nil && (!exists || h != *c.BeforeHash) {
			return errors.New("file changed after admission")
		}
		if c.ContentBase64 == nil {
			if err = root.Remove(c.Path); err != nil {
				return err
			}
			continue
		}
		parent := path.Dir(c.Path)
		if parent != "." {
			if err = safepath.EnsureDirectory(b.Request.Path, parent); err != nil {
				return err
			}
		}
		if err = safepath.Check(root, c.Path, true); err != nil {
			return err
		}
		data, err := content(c)
		if err != nil {
			return err
		}
		temp := fmt.Sprintf(".harness-tmp-%s-%d", id, i)
		mode := os.FileMode(0644)
		if c.Executable {
			mode = 0755
		}
		f, err := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		n, writeErr := f.Write(data)
		if writeErr == nil && n != len(data) {
			writeErr = errors.New("short file write")
		}
		if writeErr == nil {
			writeErr = f.Sync()
		}
		closeErr := f.Close()
		if err = errors.Join(writeErr, closeErr); err != nil {
			return err
		}
		// Rename replaces the directory entry instead of writing through an alias.
		// Recheck the path after preparing bytes; rooted rename contains traversal.
		if err = safepath.Check(root, c.Path, true); err != nil {
			return err
		}
		fresh, _, _, present, err := safepath.ReadRegular(root, c.Path, 64<<20)
		if err != nil {
			return err
		}
		if c.BeforeHash == nil && present || c.BeforeHash != nil && (!present || fresh != *c.BeforeHash) {
			return errors.New("target changed while preparing replacement")
		}
		if err = root.Rename(temp, c.Path); err != nil {
			return err
		}
	}
	return nil
}

// Classify compares whole observed source state, not only the edited paths.
// Nil observation or partial/unexpected changes remain UNKNOWN.
func Classify(p Proposal, c *worktree.Candidate) string {
	if c == nil {
		return "UNKNOWN"
	}
	if *c == p.After {
		return "CONFIRMED"
	}
	if *c == p.Before {
		return "NOT_APPLIED"
	}
	return "UNKNOWN"
}
