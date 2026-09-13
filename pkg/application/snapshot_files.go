package application

import (
	"context"
	"io/fs"
	"iter"
	"strings"

	"github.com/networkteam/sdd/internal/meta"
	"github.com/networkteam/sdd/internal/model"
)

// SnapshotFile describes a non-directory source path without reading its content.
type SnapshotFile struct {
	// Path is relative to the supplied filesystem root, including graphDir.
	Path string
	// Document marks entry and WIP paths whose content LoadSnapshotFS reads.
	Document bool
}

// WalkSnapshotFiles inventories graph files without opening their content. An
// empty graphDir means ".". Nested .sdd directories are skipped. Stopping the
// iterator stops traversal; a walk or context error ends the sequence.
func WalkSnapshotFiles(ctx context.Context, fsys fs.FS, graphDir string) iter.Seq2[SnapshotFile, error] {
	if graphDir == "" {
		graphDir = "."
	}
	return func(yield func(SnapshotFile, error) bool) {
		if err := ctx.Err(); err != nil {
			yield(SnapshotFile{}, err)
			return
		}
		err := fs.WalkDir(fsys, graphDir, func(filename string, entry fs.DirEntry, walkErr error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if filename != graphDir && meta.IsSDDMetaDir(entry) {
					return fs.SkipDir
				}
				return nil
			}
			document := false
			if strings.HasSuffix(entry.Name(), ".md") {
				rel := snapshotRelativePath(filename, graphDir)
				_, pathErr := model.RelPathToID(rel)
				document = strings.HasPrefix(rel, "wip/") || pathErr == nil
			}
			if !yield(SnapshotFile{Path: filename, Document: document}, nil) {
				return fs.SkipAll
			}
			return nil
		})
		if err != nil {
			yield(SnapshotFile{}, err)
		}
	}
}

func snapshotRelativePath(filename, graphDir string) string {
	if graphDir == "." {
		return strings.TrimPrefix(filename, "./")
	}
	return strings.TrimPrefix(filename, strings.TrimSuffix(graphDir, "/")+"/")
}
