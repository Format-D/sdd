package local

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/networkteam/sdd/internal/meta"
	"github.com/networkteam/sdd/internal/model"
	"github.com/networkteam/sdd/internal/repos"
	app "github.com/networkteam/sdd/pkg/application"
)

// NewRepositoryTargets opens configured graph readers and Git-backed mutation
// targets. Each acquisition rereads the global, committed and local config.
func NewRepositoryTargets(project app.ProjectID, serverCheckout, globalConfigPath string) (*GitWorktreeAcquirer, error) {
	resolveConfig := func(checkout string) (*model.PerRepoConfig, error) {
		global, err := repos.LoadConfigFrom(globalConfigPath)
		if err != nil {
			return nil, err
		}
		return meta.ResolveConfig(global.BaseConfig, filepath.Join(checkout, model.SDDDirName))
	}
	return NewGitWorktreeAcquirer(GitWorktreeAcquirerOptions{
		Project: project, ServerCheckout: serverCheckout,
		ReadFactory: func(ctx context.Context, checkout string, q app.SnapshotReadQuery) (*app.AcquiredSnapshot, error) {
			cfg, err := resolveConfig(checkout)
			if err != nil {
				return nil, err
			}
			if cfg == nil {
				return nil, fmt.Errorf("read checkout %q has no SDD configuration", checkout)
			}
			id := app.ProjectID(cfg.RepoID)
			if id == "" {
				id = "local"
			}
			if id != project {
				return nil, fmt.Errorf("read checkout %q does not contain project %s", checkout, project)
			}
			graph, err := NewFilesystemGraphStore(FilesystemGraphStoreOptions{
				Project: project, GraphDir: meta.ResolveGraphDir(checkout, cfg), Branch: q.Branch,
			})
			if err != nil {
				return nil, err
			}
			return graph.AcquireSnapshot(ctx, q)
		},
		Factory: func(_ context.Context, checkout string, target app.MutationTarget) (app.GraphStore, []app.MutationFinalizer, func() error, error) {
			cfg, err := resolveConfig(checkout)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("loading mutation target config for %s: %w", target.Branch, err)
			}
			if cfg == nil {
				return nil, nil, nil, fmt.Errorf("mutation target checkout %q does not contain project %s", checkout, project)
			}
			id := app.ProjectID(cfg.RepoID)
			if id == "" {
				id = "local"
			}
			if id != project {
				return nil, nil, nil, fmt.Errorf("mutation target checkout %q does not contain project %s", checkout, project)
			}
			graphDir := cfg.GraphDir
			if graphDir == "" {
				graphDir = model.DefaultGraphDir
			}
			finalizer := GitFinalizer{Checkout: checkout, GraphDir: graphDir, Branch: target.Branch}
			graph, err := NewFilesystemGraphStore(FilesystemGraphStoreOptions{
				Project: project, GraphDir: meta.ResolveGraphDir(checkout, cfg), Branch: target.Branch, PublicationGit: &finalizer,
			})
			if err != nil {
				return nil, nil, nil, err
			}
			return graph, []app.MutationFinalizer{finalizer}, func() error { return nil }, nil
		},
	})
}
