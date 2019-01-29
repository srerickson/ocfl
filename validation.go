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
	"context"
	"log"
	"os"
	"path/filepath"
)

// sendErr sends the error over the channel if ctx is still active.
// It returns true if the error was sent, false otherwise.
func sendErr(ctx context.Context, errs chan error, err error) bool {
	select {
	case <-ctx.Done():
		return false
	case errs <- err:
		return true
	}
}

func ctxDone(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	default:
		return false
	}
}

// ValidateObject validates the object at path. It returns
// only the first error encountered, canceling any
// remaining validation tests
func ValidateObject(path string) error {
	var err error
	obj, err := GetObject(path)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	for err = range obj.Validate(ctx) {
		break
	}
	// good practice to check that context wasn't canceled
	// if ctxErr := ctx.Err(); ctxErr != nil {
	// 	err = ctxErr
	// }
	cancel()
	return err
}

// Validate runs all validation tests on an object within
// the given context. It returns a channel to receive
// validation errors and a function that returns a closed
// channel when all tests are completed. If the context
// is canceled before all tests are complete, the complete
// function remains open.
func (obj *Object) Validate(ctx context.Context) chan error {
	errs := make(chan error)

	go func() {
		defer close(errs)

		inv := &(obj.inventory)
		alg := inv.DigestAlgorithm
		man := inv.Manifest
		path := obj.Path

		// validate inventory structure
		var invErr error
		for invErr = range inv.validateStructure(ctx) {
			if !sendErr(ctx, errs, invErr) {
				return
			}
		}

		// don't continue if inventory is broken
		if invErr != nil {
			return
		}

		// validate version directories
		vDirs, err := obj.versionDirs()
		if err != nil && !sendErr(ctx, errs, err) {
			return
		}
		for _, dir := range vDirs {
			for err := range obj.validateVerDir(ctx, dir) {
				if !sendErr(ctx, errs, err) {
					return
				}
			}

		}
		//Manifest Checksum
		for err := range man.Validate(ctx, obj.Path, alg) {
			if !sendErr(ctx, errs, err) {
				return
			}
		}
		//Fixity Checksum
		for alg, manifest := range inv.Fixity {
			for err := range manifest.Validate(ctx, path, alg) {
				if !sendErr(ctx, errs, err) {
					return
				}
			}
		}
	}()
	return errs
}

func (obj *Object) validateVerDir(ctx context.Context, ver string) chan error {
	errs := make(chan error)

	go func() {
		defer close(errs)

		invPath := filepath.Join(obj.Path, ver, inventoryFileName)
		inv, err := ReadValidateInventory(invPath)

		if os.IsNotExist(err) {
			log.Printf(`WARNING: Version %s has not inventory`, ver)
		} else if err != nil {
			sendErr(ctx, errs, err)
		} else {
			for err := range inv.validateStructure(ctx) {
				if !sendErr(ctx, errs, err) {
					return
				}
			}
		}

		// Check version content present in manifest
		contPath := filepath.Join(obj.Path, ver, `content`)
		walk := func(fPath string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			ePath, pathErr := filepath.Rel(obj.Path, fPath)
			if pathErr != nil {
				return pathErr
			}
			if obj.inventory.Manifest.GetDigest(ePath) == `` {
				sendErr(ctx, errs, NewErr(ManPathErr, nil))
			}
			return nil
		}
		filepath.Walk(contPath, walk)
	}()
	return errs
}

// Validate returns a channel of checksum validation errors
func (cm ContentMap) Validate(ctx context.Context, dir string, alg string) chan error {
	in := make(chan checksumJob)
	errs := make(chan error)
	go func() {
		defer close(in)
		for file := range cm.Iterate() {
			select {
			case <-ctx.Done():
				// drain cm Iterate
			case in <- checksumJob{
				path:     filepath.Join(dir, file.Path),
				alg:      alg,
				expected: file.Digest,
			}:
			}
		}
	}()
	go func() {
		defer close(errs)
		for result := range digester(ctx, in) {
			select {
			case <-ctx.Done():
				errs <- NewErr(CtxCanceledErr, nil)
				return
			default:
				if result.err != nil {
					errs <- result.err
				} else if result.sum != result.expected {
					// FIXME: include path in error
					errs <- NewErr(ContentChecksumErr, nil)
				}
			}
		}
	}()
	return errs
}

// validateInventory really just checks consistency of the inventory
func (inv *Inventory) validateStructure(ctx context.Context) chan error {
	errs := make(chan error)

	go func() {
		defer close(errs)
		// Validate Inventory Structure:
		if inv.ID == `` {
			if !sendErr(ctx, errs, NewErr(InvIDErr, nil)) {
				return
			}
		}
		if inv.Type != inventoryType {
			if !sendErr(ctx, errs, NewErr(InvTypeErr, nil)) {
				return
			}
		}
		if inv.DigestAlgorithm == `` {
			if !sendErr(ctx, errs, NewErr(InvDigestErr, nil)) {
				return
			}
		} else if !stringIn(inv.DigestAlgorithm, digestAlgorithms[:]) {
			if !sendErr(ctx, errs, NewErr(InvDigestErr, nil)) {
				return
			}
		}
		if inv.Manifest == nil {
			if !sendErr(ctx, errs, NewErr(InvNoManErr, nil)) {
				return
			}
		}
		if inv.Versions == nil {
			if !sendErr(ctx, errs, NewErr(InvNoVerErr, nil)) {
				return
			}
		}
		// Validate Version Names in Inventory
		var versions = inv.versionNames()
		var padding int
		if len(inv.Versions) > 0 {
			padding = versionPadding(versions[0])
			for i := range versions {
				n, _ := versionGen(i+1, padding)
				if _, ok := inv.Versions[n]; !ok {
					if !sendErr(ctx, errs, NewErr(VerFormatErr, nil)) {
						return
					}
				}
			}
		}
		// make sure every digest in version state is present in the manifest
		for vname := range inv.Versions {
			for digest := range inv.Versions[vname].State {
				if inv.Manifest.LenDigest(digest) == 0 {
					if !sendErr(ctx, errs, NewErr(ManDigestErr, nil)) {
						return
					}
				}
			}
		}
	}()
	return errs
}
