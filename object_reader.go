package ocfl

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"

	"github.com/srerickson/checksum"
)

const (
	ocflVersion           = "1.0"
	objectDeclaration     = `ocfl_object_` + ocflVersion
	objectDeclarationFile = `0=ocfl_object_` + ocflVersion
	inventoryFile         = `inventory.json`
)

// ObjectReader represents a readable OCFL Object
type ObjectReader struct {
	root       fs.FS // root fs
	*Inventory       // inventory.json
}

// NewObjectReader returns a new ObjectReader with loaded inventory.
// An error is returned only if the inventory cannot be unmarshaled
func NewObjectReader(root fs.FS) (*ObjectReader, error) {
	obj := &ObjectReader{root: root}
	err := obj.readDeclaration()
	if err != nil {
		return nil, err
	}
	obj.Inventory, err = obj.readInventory(`.`)
	if err != nil {
		return nil, err
	}

	return obj, nil
}

// readDeclaration reads and validates the declaration file
func (obj *ObjectReader) readDeclaration() error {
	f, err := obj.root.Open(objectDeclarationFile)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			err = fmt.Errorf(`version declaration not found: %w`, &ErrE003)
		}
		return err
	}
	defer f.Close()
	decl, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	if string(decl) != objectDeclaration+"\n" {
		return fmt.Errorf(`version declaration invalid: %w`, &ErrE007)
	}
	return nil
}

func (obj *ObjectReader) readInventory(dir string) (*Inventory, error) {
	path := filepath.Join(dir, inventoryFile)
	file, err := obj.root.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, &ErrE034
		}
		return nil, err
	}
	defer file.Close()
	return ReadInventory(file)
}

type fsOpenFunc func(name string) (fs.File, error)

func (f fsOpenFunc) Open(name string) (fs.File, error) {
	return f(name)
}

// VersionFS returns an fs.FS representing the logical state of the version
func (obj *ObjectReader) VersionFS(vname string) (fs.FS, error) {
	v, ok := obj.Inventory.Versions[vname]
	if !ok {
		return nil, fmt.Errorf(`Version not found: %s`, vname)
	}
	var open fsOpenFunc = func(logicalPath string) (fs.File, error) {
		digest := v.State.GetDigest(logicalPath)
		if digest == "" {
			return nil, fmt.Errorf(`%s: %w`, logicalPath, fs.ErrNotExist)
		}
		realpaths := obj.Manifest[digest]
		if len(realpaths) == 0 {
			return nil, fmt.Errorf(`no manifest entries files associated with the digest: %s`, digest)
		}
		return obj.root.Open(filepath.FromSlash(realpaths[0]))
	}
	return open, nil
}

// Content returns DigestMap of all version contents
func (obj *ObjectReader) Content() (DigestMap, error) {
	var content DigestMap
	alg := obj.DigestAlgorithm
	newH, err := newHash(alg)
	if err != nil {
		return nil, err
	}
	each := func(j checksum.Job, err error) error {
		if err != nil {
			return err
		}
		sum, err := j.SumString(alg)
		if err != nil {
			return err
		}
		return content.Add(sum, j.Path())
	}
	for v := range obj.Inventory.Versions {
		contentDir := filepath.Join(v, obj.ContentDirectory)
		// contentDir may not exist - that's ok
		err = checksum.Walk(obj.root, contentDir, each, checksum.WithAlg(alg, newH))
		if err != nil {
			walkErr, _ := err.(*checksum.WalkErr)
			if errors.Is(walkErr.WalkDirErr, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
	}
	return content, nil
}
