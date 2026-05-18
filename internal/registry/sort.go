package registry

import (
	"sort"
	"strconv"
	"strings"
)

// sortWorkersByPane orders workers by numeric pane id (so "%9" sorts
// before "%10", not after). Non-numeric pane ids fall back to lexical.
func sortWorkersByPane(ws []Worker) {
	sort.SliceStable(ws, func(i, j int) bool {
		ni, oki := paneNumeric(ws[i].PaneID)
		nj, okj := paneNumeric(ws[j].PaneID)
		if oki && okj {
			return ni < nj
		}
		return ws[i].PaneID < ws[j].PaneID
	})
}

func paneNumeric(pane string) (int, bool) {
	if !strings.HasPrefix(pane, "%") {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimPrefix(pane, "%"))
	if err != nil {
		return 0, false
	}
	return n, true
}
