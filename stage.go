// Copyright 2019 Seth R. Erickson
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ocfl

import (
	"errors"
	"io/ioutil"
	"os"
	"path/filepath"
	"time"
)

var (
	// FILEMODE is default FileMode for new files
	FILEMODE os.FileMode = 0644
	// DIRMODE is default FileMode for new directories
	DIRMODE os.FileMode = 0755
)

// Stage represents a staging area for creating new Object Versions
type Stage struct {
	State  ContentMap // next version state
	Path   string     // tmp directory for staging new files
	object *Object    // parent object
}

func (stage *Stage) clear() {
	if stage == nil {
		return
	}
	if stage.Path != `` {
		os.RemoveAll(stage.Path)
		stage.Path = ``
	}
	stage.State = nil
}

// Commit creates a new Version in the Stage's parent Object reflecting
// changes made through the Stage.
func (stage *Stage) Commit(user User, message string) error {
	if stage.object == nil {
		return errors.New(`stage has no parent object`)
	}
	if stage.State == nil {
		return errors.New(`stage has no state`)
	}
	nextVer, err := stage.object.nextVersion()
	if err != nil {
		return err
	}
	// move tmpdir to version/contents
	verDir := filepath.Join(stage.object.Path, nextVer)
	if err := os.Mkdir(verDir, DIRMODE); err != nil {
		return err
	}
	// if stage has new content, move into version/content dir
	// TODO: if there any empty files in stage dir, delete them
	if stage.Path != `` {
		if newFiles, err := ioutil.ReadDir(stage.Path); err != nil {
			return err
		} else if len(newFiles) > 0 {
			verContDir := filepath.Join(verDir, `content`)
			if err := os.Rename(stage.Path, verContDir); err != nil {
				return err
			}
			walk := func(path string, info os.FileInfo, walkErr error) error {
				if walkErr == nil && info.Mode().IsRegular() {
					alg := stage.object.inventory.DigestAlgorithm
					digest, digestErr := Checksum(alg, path)
					if digestErr != nil {
						return digestErr
					}
					ePath, pathErr := filepath.Rel(stage.object.Path, path)
					if pathErr != nil {
						return pathErr
					}
					vPath, pathErr := filepath.Rel(verContDir, path)
					if pathErr != nil {
						return pathErr
					}
					stage.State.AddReplace(Digest(digest), Path(vPath))
					stage.object.inventory.Manifest.Add(Digest(digest), Path(ePath))
				}
				return walkErr
			}
			filepath.Walk(verContDir, walk)

		}
	}

	newVersion := NewVersion()
	newVersion.State = stage.State.Copy()
	newVersion.User = user
	newVersion.Message = message
	newVersion.Created = time.Now()

	// update inventory
	stage.object.inventory.Versions[nextVer] = newVersion
	stage.object.inventory.Head = nextVer

	// write inventory (twice)
	if err := stage.object.writeInventoryVersion(nextVer); err != nil {
		return err
	}
	return stage.object.writeInventory()
}

// OpenFile returns a readable and writable *os.File for the given Logical Path.
// If the file has not already been staged (which is the case even if the file
// exists in the current Version State), it is created, along with all parent
// directories. It should not be used to read already committed files: use
// Object.Open() instead.
func (stage *Stage) OpenFile(lPath string) (*os.File, error) {
	if stage.Path == `` {
		dir, err := ioutil.TempDir(stage.object.Path, `stage`)
		if err != nil {
			return nil, err
		}
		stage.Path = dir
	}
	fullPath := stage.fullPath(lPath)
	dir := filepath.Dir(fullPath)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		os.MkdirAll(dir, DIRMODE)
	} else {
		return nil, err
	}
	return os.OpenFile(fullPath, os.O_RDWR|os.O_CREATE, FILEMODE)
}

// Rename renames files that are staged or that exist in the staged version
func (stage *Stage) Rename(src string, dst string) error {
	var renamedStaged bool
	if stage.isStaged(src) {
		err := os.Rename(stage.fullPath(src), stage.fullPath(dst))
		if err != nil {
			return err
		}
		renamedStaged = true
	}
	err := stage.State.Rename(Path(src), Path(dst))
	if err != nil && !renamedStaged {
		return err
	}
	return nil
}

// Remove removes files that are staged or that exist in the staged version
func (stage *Stage) Remove(lPath string) error {
	var removedStaged bool
	if stage.isStaged(lPath) {
		err := os.Remove(stage.fullPath(lPath))
		if err != nil {
			return err
		}
		removedStaged = true
	}
	_, err := stage.State.Remove(Path(lPath))
	if err != nil && !removedStaged {
		return err
	}
	return nil
}

// fullPath gives return the real path from the logical path for a
// staged file. The file does not necessarily exist
func (stage *Stage) fullPath(lPath string) string {
	return filepath.Join(stage.Path, lPath)
}

// isStaged returns whether the lPath exists as a new/modified file in the stage
func (stage *Stage) isStaged(lPath string) bool {
	_, err := os.Stat(stage.fullPath(lPath))
	return !os.IsNotExist(err)
}
