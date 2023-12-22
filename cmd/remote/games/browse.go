package games

import (
	"encoding/json"
	"github.com/wizzomafizzo/mrext/cmd/remote/menu"
	"github.com/wizzomafizzo/mrext/pkg/config"
	"github.com/wizzomafizzo/mrext/pkg/games"
	"github.com/wizzomafizzo/mrext/pkg/service"
	"github.com/wizzomafizzo/mrext/pkg/utils"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type FolderResult struct {
	System games.System `json:"system"`
	Path   string       `json:"path"`
}

func getGamesFolders() []FolderResult {
	systemResults := make(map[string]FolderResult)
	folderNames := make(map[string]games.System)

	for _, system := range games.Systems {
		folder := strings.ToLower(system.Folder[0])
		folderNames[folder] = system
	}

	for _, root := range config.GamesFolders {
		if _, err := os.Stat(root); err != nil {
			continue
		}

		gfs, err := os.ReadDir(root)
		if err != nil {
			continue
		}

		for _, gf := range gfs {
			if !gf.IsDir() {
				continue
			}

			folder := strings.ToLower(gf.Name())

			if _, ok := folderNames[folder]; !ok {
				continue
			}

			system := folderNames[folder]

			_, ok := systemResults[system.Id]
			if ok {
				continue
			}

			systemResults[system.Id] = FolderResult{
				System: system,
				Path:   filepath.Join(root, gf.Name()),
			}
		}
	}

	folders := make([]FolderResult, 0)
	for _, result := range systemResults {
		folders = append(folders, result)
	}

	return folders
}

type fileEntry struct {
	path    string
	name    string
	size    int64
	isDir   bool
	modTime time.Time
}

type ZipCache struct {
	mu  sync.Mutex
	Map map[string][]string
}

func (zc *ZipCache) Get(path string) ([]string, bool) {
	zc.mu.Lock()
	defer zc.mu.Unlock()

	if zc.Map == nil {
		zc.Map = make(map[string][]string)
	}

	paths, ok := zc.Map[path]
	return paths, ok
}

func (zc *ZipCache) Set(path string, files []string) {
	zc.mu.Lock()
	defer zc.mu.Unlock()

	if zc.Map == nil {
		zc.Map = make(map[string][]string)
	}

	zc.Map[path] = files
}

var zipCache = &ZipCache{}

func listPath(logger *service.Logger, path string) ([]menu.Item, error) {
	system, err := games.BestSystemMatch(&config.UserConfig{}, path)
	if err != nil {
		return nil, err
	}
	logger.Info("system: %s", system.Id)

	inZip := false
	zipIndex := -1
	parts := strings.Split(path, "/")

	for i, part := range parts {
		if strings.HasSuffix(strings.ToLower(part), ".zip") {
			inZip = true
			zipIndex = i
			break
		}
	}

	files := make([]fileEntry, 0)

	if inZip {
		zipFile := strings.Join(parts[:zipIndex+1], "/")
		zipPath := strings.Join(parts[zipIndex+1:], "/")

		paths, ok := zipCache.Get(zipFile)
		if ok {
			logger.Info("found cached zip paths")
			for _, ps := range paths {
				if !strings.HasPrefix(path, zipPath) {
					continue
				}

				if strings.Count(path, "/") > strings.Count(zipPath, "/") {
					continue
				}

				files = append(files, fileEntry{
					path:    ps,
					name:    filepath.Base(ps),
					isDir:   false,
					modTime: time.Time{},
				})
			}
		} else {
			logger.Info("no cached zip paths found")
			zipFiles, err := utils.ListZip(zipFile)
			if err != nil {
				return nil, err
			}
			zipCache.Set(zipFile, zipFiles)

			for _, ps := range zipFiles {
				if !strings.HasPrefix(ps, zipPath) {
					continue
				}

				if strings.Count(ps, "/") > strings.Count(zipPath, "/") {
					continue
				}

				files = append(files, fileEntry{
					path:    ps,
					name:    filepath.Base(ps),
					isDir:   false,
					modTime: time.Time{},
				})
			}
		}
	} else {
		fsFiles, err := os.ReadDir(path)
		if err != nil {
			return nil, err
		}

		for _, fsFile := range fsFiles {
			info, err := fsFile.Info()
			if err != nil {
				continue
			}

			files = append(files, fileEntry{
				path:    filepath.Join(path, fsFile.Name()),
				name:    fsFile.Name(),
				size:    info.Size(),
				isDir:   fsFile.IsDir(),
				modTime: info.ModTime(),
			})
		}
	}

	validFiletypes := make([]string, 0)

	if !inZip {
		validFiletypes = append(validFiletypes, ".zip")
	}

	for _, slot := range system.Slots {
		for _, filetype := range slot.Exts {
			validFiletypes = append(validFiletypes, filetype)
		}
	}
	logger.Info("valid filetypes: %s", validFiletypes)

	items := make([]menu.Item, 0)

	for _, file := range files {
		friendlyName := strings.TrimSuffix(file.name, filepath.Ext(file.name))

		if !file.isDir && !utils.Contains(validFiletypes, filepath.Ext(file.name)) {
			continue
		}

		var next *string
		if file.isDir {
			nextPath := filepath.Join(path, file.name)
			next = &nextPath
		}

		items = append(items, menu.Item{
			Name:      friendlyName,
			Path:      filepath.Join(path, file.name),
			Parent:    path,
			Filename:  filepath.Base(file.name),
			Extension: filepath.Ext(file.name),
			Next:      next,
			Modified:  file.modTime,
			Size:      file.size,
		})
	}

	return items, nil
}

type ListGamesPayload struct {
	Up    *string     `json:"up,omitempty"`
	Items []menu.Item `json:"items"`
	// TODO: system
}

func ListGamesFolder(logger *service.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		logger.Info("list games folder")

		var args struct {
			Path string `json:"path"`
		}

		err := json.NewDecoder(r.Body).Decode(&args)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			logger.Error("error decoding request: %s", err)
			return
		}

		items := make([]menu.Item, 0)
		var up *string

		systemFolders := getGamesFolders()
		systemFoldersMap := make(map[string]bool)
		for _, folder := range systemFolders {
			systemFoldersMap[strings.ToLower(folder.Path)] = true
		}

		// list system folders instead
		if args.Path == "" {
			up = nil
			for _, folder := range systemFolders {
				var next *string
				nextPath := folder.Path
				next = &nextPath

				items = append(items, menu.Item{
					Name:      filepath.Base(folder.Path),
					Path:      folder.Path,
					Parent:    args.Path,
					Filename:  filepath.Base(folder.Path),
					Extension: filepath.Ext(folder.Path),
					Next:      next,
					Type:      "folder",
				})
			}

			err = json.NewEncoder(w).Encode(ListGamesPayload{
				Up:    up,
				Items: items,
			})
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				logger.Error("error encoding payload: %s", err)
				return
			}

			return
		}

		path, err := filepath.Abs(args.Path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			logger.Error("error getting absolute path: %s", err)
			return
		}

		valid := false
		atRoot := false
		for _, folder := range config.GamesFolders {
			if strings.EqualFold(path, folder) {
				valid = false
				break
			}

			if strings.HasPrefix(strings.ToLower(path), folder) {
				valid = true
			}

			if _, ok := systemFoldersMap[strings.ToLower(path)]; ok {
				valid = true
				atRoot = true
				break
			}
		}

		if !valid {
			http.Error(w, "invalid path", http.StatusInternalServerError)
			logger.Error("invalid path: %s", path)
			return
		}

		if atRoot {
			home := ""
			up = &home
		} else {
			upPath := filepath.Dir(path)
			up = &upPath
		}

		items, err = listPath(logger, path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			logger.Error("error listing path: %s", err)
			return
		}

		payload := ListGamesPayload{
			Up:    up,
			Items: items,
		}

		err = json.NewEncoder(w).Encode(payload)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			logger.Error("error encoding payload: %s", err)
			return
		}
	}
}
