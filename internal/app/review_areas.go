package app

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/brymartinez/foreman/internal/task"
)

// These are soft scheduling targets, not limits on accepted code or agent output.
const reviewAreaTargetLines = 800
const maxSelfReviewAreas = 3

func planReviewAreas(files []ReviewFile, limit int) []task.ReviewArea {
	type unit struct {
		key    string
		files  []ReviewFile
		weight int
	}
	byKey := map[string]*unit{}
	total := 0
	for _, f := range files {
		stem := strings.Split(strings.TrimSuffix(path.Base(f.Path), path.Ext(f.Path)), ".")[0]
		stem = strings.TrimSuffix(strings.TrimPrefix(stem, "test_"), "_test")
		key := path.Join(path.Dir(f.Path), stem)
		if byKey[key] == nil {
			byKey[key] = &unit{key: key}
		}
		weight := max(1, f.ChangedLines)
		byKey[key].files = append(byKey[key].files, f)
		byKey[key].weight += weight
		total += weight
	}
	if total == 0 {
		return nil
	}
	count := min(limit, (total+reviewAreaTargetLines-1)/reviewAreaTargetLines, len(byKey))
	units := make([]*unit, 0, len(byKey))
	for _, u := range byKey {
		units = append(units, u)
	}
	sort.Slice(units, func(i, j int) bool {
		if units[i].weight != units[j].weight {
			return units[i].weight > units[j].weight
		}
		return units[i].key < units[j].key
	})
	areas := make([]task.ReviewArea, count)
	weights := make([]int, count)
	for _, u := range units {
		lightest := 0
		for i := range areas {
			if weights[i] < weights[lightest] {
				lightest = i
			}
		}
		for _, f := range u.files {
			areas[lightest].Paths = append(areas[lightest].Paths, f.Path)
		}
		weights[lightest] += u.weight
	}
	for i := range areas {
		sort.Strings(areas[i].Paths)
		areas[i].Name = fmt.Sprintf("%d/%d (%s)", i+1, count, strings.Join(areaDirectories(areas[i].Paths), ", "))
	}
	return areas
}

func areaDirectories(paths []string) []string {
	seen := map[string]bool{}
	var dirs []string
	for _, p := range paths {
		dir := path.Dir(p)
		if !seen[dir] {
			seen[dir] = true
			dirs = append(dirs, dir)
		}
	}
	return dirs
}
