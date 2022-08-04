package games

import (
	"fmt"
	"io/fs"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"
	s "strings"

	"github.com/wizzomafizzo/mrext/pkg/utils"
)

func GetSystem(id string) (*System, error) {
	if system, ok := SYSTEMS[id]; ok {
		return &system, nil
	} else {
		return nil, fmt.Errorf("unknown system: %s", id)
	}
}

func LookupSystem(id string) (*System, error) {
	var system *System

	for k, v := range SYSTEMS {
		if s.EqualFold(k, id) {
			system = &v
		}
	}

	if system == nil {
		return nil, fmt.Errorf("unknown system: %s", id)
	} else {
		return system, nil
	}
}

func matchSystemFolder(path string) ([][2]string, error) {
	var matches [][2]string

	folder, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	name := folder.Name()

	if !folder.IsDir() {
		return nil, fmt.Errorf("not a directory: %s", path)
	}

	for k, v := range SYSTEMS {
		if s.EqualFold(name, v.Folder) {
			matches = append(matches, [2]string{k, path})
		}
	}

	if len(matches) == 0 {
		return nil, fmt.Errorf("unknown system: %s", name)
	} else {
		return matches, nil
	}
}

func matchSystemFile(system System, path string) bool {
	for _, args := range system.FileTypes {
		for _, ext := range args.Extensions {
			if s.HasSuffix(s.ToLower(path), ext) {
				return true
			}
		}
	}
	return false
}

func findSystemFolders(path string) [][2]string {
	var found [][2]string

	root, err := os.Stat(path)
	if err != nil || !root.IsDir() {
		return nil
	}

	folders, err := ioutil.ReadDir(path)
	if err != nil {
		return nil
	}

	for _, folder := range folders {
		abs := filepath.Join(path, folder.Name())

		if folder.IsDir() && s.ToLower(folder.Name()) == "games" {
			found = append(found, findSystemFolders(abs)...)
		}

		matches, err := matchSystemFolder(abs)
		if err != nil {
			continue
		} else {
			found = append(found, matches...)
		}
	}

	return found
}

func GetSystemPaths() map[string][]string {
	var paths = make(map[string][]string)

	for _, rootPath := range GAMES_FOLDERS {
		for _, result := range findSystemFolders(rootPath) {
			paths[result[0]] = append(paths[result[0]], result[1])
		}
	}

	return paths
}

type resultsStack [][]string

func (r *resultsStack) new() {
	*r = append(*r, []string{})
}

func (r *resultsStack) pop() {
	if len(*r) == 0 {
		return
	}
	*r = (*r)[:len(*r)-1]
}

func (r *resultsStack) get() (*[]string, error) {
	if len(*r) == 0 {
		return nil, fmt.Errorf("nothing on stack")
	}
	return &(*r)[len(*r)-1], nil
}

// Search for all valid games in a given path and return a list of files.
// This function deep searches .zip files and handles symlinks at all levels.
func GetFiles(systemId string, path string) ([]string, error) {
	var allResults []string
	var stack resultsStack
	visited := make(map[string]struct{})

	system, err := GetSystem(systemId)
	if err != nil {
		return nil, err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	var scanner func(path string, file fs.DirEntry, err error) error
	scanner = func(path string, file fs.DirEntry, _ error) error {
		// avoid recursive symlinks
		if file.IsDir() {
			if _, ok := visited[path]; ok {
				return filepath.SkipDir
			} else {
				visited[path] = struct{}{}
			}
		}

		// handle symlinked directories
		if file.Type()&os.ModeSymlink != 0 {
			err = os.Chdir(filepath.Dir(path))
			if err != nil {
				return err
			}

			realPath, err := filepath.EvalSymlinks(path)
			if err != nil {
				return err
			}

			file, err := os.Stat(realPath)
			if err != nil {
				return err
			}

			if file.IsDir() {
				err = os.Chdir(path)
				if err != nil {
					return err
				}

				stack.new()

				filepath.WalkDir(realPath, scanner)

				results, err := stack.get()
				if err != nil {
					return err
				}

				for _, result := range *results {
					result = s.Replace(result, realPath, path, 1)
					allResults = append(allResults, result)
				}

				stack.pop()
				return nil
			}
		}

		results, err := stack.get()
		if err != nil {
			return err
		}

		if s.HasSuffix(s.ToLower(path), ".zip") {
			// zip files
			zipFiles, err := utils.ListZip(path)
			if err != nil {
				return err
			}

			for _, zipPath := range zipFiles {
				if matchSystemFile(*system, zipPath) {
					abs := filepath.Join(path, zipPath)
					*results = append(*results, string(abs))

				}
			}
		} else {
			// regular files
			if matchSystemFile(*system, path) {
				*results = append(*results, path)
			}
		}

		return nil
	}

	stack.new()

	root, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}

	err = os.Chdir(filepath.Dir(path))
	if err != nil {
		return nil, err
	}

	// handle symlinks on root game folder because WalkDir fails silently on them
	var realPath string
	if root.Mode()&os.ModeSymlink == 0 {
		realPath = path
	} else {
		realPath, err = filepath.EvalSymlinks(path)
		if err != nil {
			return nil, err
		}
	}

	realRoot, err := os.Stat(realPath)
	if err != nil {
		return nil, err
	}

	if !realRoot.IsDir() {
		return nil, fmt.Errorf("root is not a directory")
	}

	filepath.WalkDir(realPath, scanner)

	results, err := stack.get()
	if err != nil {
		return nil, err
	}

	allResults = append(allResults, *results...)
	stack.pop()

	// change root back to symlink
	if realPath != path {
		for i, result := range allResults {
			allResults[i] = s.Replace(result, realPath, path, 1)
		}
	}

	err = os.Chdir(cwd)
	if err != nil {
		return nil, err
	}

	return allResults, nil
}

func GetAllFiles(systemPaths map[string][]string, statusFn func(systemId string, path string)) ([][2]string, error) {
	var allFiles [][2]string

	for systemId, paths := range systemPaths {
		for _, path := range paths {
			statusFn(systemId, path)

			files, err := GetFiles(systemId, path)
			if err != nil {
				return nil, err
			}

			for _, file := range files {
				allFiles = append(allFiles, [2]string{systemId, file})
			}
		}
	}

	return allFiles, nil
}

func FilterUniqueFilenames(files []string) []string {
	var filtered []string
	filenames := make(map[string]struct{})
	for _, file := range files {
		fn := filepath.Base(file)
		if _, ok := filenames[fn]; ok {
			continue
		} else {
			filenames[fn] = struct{}{}
			filtered = append(filtered, file)
		}
	}
	return filtered
}

var zipRe = regexp.MustCompile(`^(.*\.zip)/(.+)$`)

func FileExists(path string) bool {
	_, err := os.Stat(path)
	if err == nil {
		return true
	}

	zipMatch := zipRe.FindStringSubmatch(path)
	if zipMatch != nil {
		zipPath := zipMatch[1]
		file := zipMatch[2]

		zipFiles, err := utils.ListZip(zipPath)
		if err != nil {
			return false
		}

		for _, zipFile := range zipFiles {
			if zipFile == file {
				return true
			}
		}
	}

	return false
}

type fileChecker struct {
	zipCache map[string]map[string]struct{}
}

func (fc *fileChecker) cacheZip(zipPath string, files []string) {
	fc.zipCache[zipPath] = make(map[string]struct{})
	for _, file := range files {
		fc.zipCache[zipPath][file] = struct{}{}
	}
}

func (fc *fileChecker) existsZip(zipPath string, file string) bool {
	if _, ok := fc.zipCache[zipPath]; !ok {
		files, err := utils.ListZip(zipPath)
		if err != nil {
			return false
		}

		fc.cacheZip(zipPath, files)
	}

	if _, ok := fc.zipCache[zipPath][file]; !ok {
		return false
	}

	return true
}

func (fc *fileChecker) Exists(path string) bool {
	_, err := os.Stat(path)
	if err == nil {
		return true
	}

	zipMatch := zipRe.FindStringSubmatch(path)
	if zipMatch != nil {
		zipPath := zipMatch[1]
		file := zipMatch[2]

		return fc.existsZip(zipPath, file)
	}

	return false
}

func NewFileChecker() *fileChecker {
	return &fileChecker{
		zipCache: make(map[string]map[string]struct{}),
	}
}
