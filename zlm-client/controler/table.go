package controler

import (
	"net/url"
	"sort"
	"strconv"
	"strings"
	"zlm-admin/service"

	"github.com/gin-gonic/gin"
)

const defaultPageSize = 20

type listQuery struct {
	Q    string
	Sort string
	Dir  string
	Page int
	Size int
}

type pagerLink struct {
	N   int
	URL string
	On  bool
}

type pagerView struct {
	Page    int
	Pages   int
	Total   int
	Size    int
	PrevURL string
	NextURL string
	Links   []pagerLink
}

func parseListQuery(c *gin.Context, defaultSort, defaultDir string) listQuery {
	q := strings.TrimSpace(c.Query("q"))
	if v, ok := c.Get("list_q"); ok {
		q = strings.TrimSpace(asStr(v))
	}
	sortKey := strings.TrimSpace(c.Query("sort"))
	if v, ok := c.Get("list_sort"); ok && asStr(v) != "" {
		sortKey = strings.TrimSpace(asStr(v))
	}
	if sortKey == "" {
		sortKey = defaultSort
	}
	dir := strings.ToLower(strings.TrimSpace(c.Query("dir")))
	if v, ok := c.Get("list_dir"); ok && asStr(v) != "" {
		dir = strings.ToLower(strings.TrimSpace(asStr(v)))
	}
	if dir != "asc" && dir != "desc" {
		dir = defaultDir
	}
	page := asI(c.Query("page"))
	if v, ok := c.Get("list_page"); ok {
		page = asI(v)
	}
	size := asI(c.Query("size"))
	if v, ok := c.Get("list_size"); ok {
		size = asI(v)
	}
	return listQuery{Q: q, Sort: sortKey, Dir: dir, Page: page, Size: size}
}

func cloneValues(src url.Values) url.Values {
	out := url.Values{}
	if src == nil {
		return out
	}
	for k, vs := range src {
		cp := make([]string, len(vs))
		copy(cp, vs)
		out[k] = cp
	}
	return out
}

func withQuery(path string, q url.Values, kv ...string) string {
	out := cloneValues(q)
	for i := 0; i+1 < len(kv); i += 2 {
		k, v := kv[i], kv[i+1]
		if strings.TrimSpace(v) == "" {
			out.Del(k)
		} else {
			out.Set(k, v)
		}
	}
	enc := out.Encode()
	if enc == "" {
		return path
	}
	return path + "?" + enc
}

func sortHeaderURL(path string, q url.Values, key, curSort, curDir any) string {
	k, cur, dir := asStr(key), asStr(curSort), asStr(curDir)
	next := "desc"
	if cur == k && dir != "asc" {
		next = "asc"
	}
	return withQuery(path, q, "sort", k, "dir", next, "page", "1")
}

func buildPager(path string, q url.Values, total, page, size int) pagerView {
	if size <= 0 {
		size = defaultPageSize
	}
	if size > 100 {
		size = 100
	}
	pages := 1
	if total > 0 {
		pages = (total + size - 1) / size
	}
	if page < 1 {
		page = 1
	}
	if page > pages {
		page = pages
	}
	view := pagerView{Page: page, Pages: pages, Total: total, Size: size}
	if pages <= 1 {
		return view
	}
	if page > 1 {
		view.PrevURL = withQuery(path, q, "page", strconv.Itoa(page-1))
	}
	if page < pages {
		view.NextURL = withQuery(path, q, "page", strconv.Itoa(page+1))
	}
	start, end := page-2, page+2
	if start < 1 {
		start = 1
	}
	if end > pages {
		end = pages
	}
	for n := start; n <= end; n++ {
		view.Links = append(view.Links, pagerLink{
			N: n, URL: withQuery(path, q, "page", strconv.Itoa(n)), On: n == page,
		})
	}
	return view
}

func paginateMediaFiles(files []service.MediaFile, page, size int) ([]service.MediaFile, int, int) {
	total := len(files)
	if size <= 0 {
		size = defaultPageSize
	}
	if size > 100 {
		size = 100
	}
	pages := 1
	if total > 0 {
		pages = (total + size - 1) / size
	}
	if page < 1 {
		page = 1
	}
	if page > pages {
		page = pages
	}
	start := (page - 1) * size
	if start > total {
		return []service.MediaFile{}, page, size
	}
	end := start + size
	if end > total {
		end = total
	}
	return files[start:end], page, size
}

func paginateMaps(rows []map[string]any, page, size int) ([]map[string]any, int, int) {
	total := len(rows)
	if size <= 0 {
		size = defaultPageSize
	}
	if size > 100 {
		size = 100
	}
	pages := 1
	if total > 0 {
		pages = (total + size - 1) / size
	}
	if page < 1 {
		page = 1
	}
	if page > pages {
		page = pages
	}
	start := (page - 1) * size
	if start > total {
		return []map[string]any{}, page, size
	}
	end := start + size
	if end > total {
		end = total
	}
	return rows[start:end], page, size
}

func mediaFilesOf(v any) []service.MediaFile {
	switch t := v.(type) {
	case []service.MediaFile:
		return t
	case []any:
		out := make([]service.MediaFile, 0, len(t))
		for _, x := range t {
			if f, ok := x.(service.MediaFile); ok {
				out = append(out, f)
			}
		}
		return out
	default:
		return nil
	}
}

func filterMediaFilesByPanel(files []service.MediaFile, panel string) []service.MediaFile {
	panel = strings.ToLower(strings.TrimSpace(panel))
	if panel == "vod" {
		return files
	}
	if panel == "" {
		panel = "record"
	}
	out := make([]service.MediaFile, 0, len(files))
	for _, f := range files {
		if panel == "event" {
			if f.Role == "rec_event" {
				out = append(out, f)
			}
			continue
		}
		if f.Role == "rec_event" {
			continue
		}
		out = append(out, f)
	}
	return out
}

func filterMediaFiles(files []service.MediaFile, q string) []service.MediaFile {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return files
	}
	out := make([]service.MediaFile, 0, len(files))
	for _, f := range files {
		blob := strings.ToLower(f.Name + " " + f.Dir + " " + f.Role + " " + f.Ext + " " + f.Kind)
		if strings.Contains(blob, q) {
			out = append(out, f)
		}
	}
	return out
}

func sortMediaFiles(files []service.MediaFile, key, dir string) []service.MediaFile {
	less := func(i, j int) bool {
		a, b := files[i], files[j]
		var cmp int
		switch key {
		case "name":
			cmp = strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		case "dir":
			cmp = strings.Compare(strings.ToLower(a.Dir), strings.ToLower(b.Dir))
		case "type", "role":
			cmp = strings.Compare(a.Role+a.Ext, b.Role+b.Ext)
		case "size":
			switch {
			case a.Size < b.Size:
				cmp = -1
			case a.Size > b.Size:
				cmp = 1
			}
		case "dur", "duration":
			switch {
			case a.DurationSec < b.DurationSec:
				cmp = -1
			case a.DurationSec > b.DurationSec:
				cmp = 1
			}
		default:
			cmp = strings.Compare(a.ModTime, b.ModTime)
		}
		if cmp == 0 {
			cmp = strings.Compare(a.Path, b.Path)
		}
		if dir == "asc" {
			return cmp < 0
		}
		return cmp > 0
	}
	sort.SliceStable(files, less)
	return files
}

func sessionMediaKeyOf(row map[string]any) string {
	if k := asStr(row["media_key"]); k != "" {
		return k
	}
	app, stream := strings.TrimSpace(asStr(row["app"])), strings.TrimSpace(asStr(row["stream"]))
	if app != "" && stream != "" {
		return app + "/" + stream
	}
	if stream != "" {
		return stream
	}
	return app
}

func sessionRoleRank(role string) int {
	switch role {
	case "推流":
		return 0
	case "拉流":
		return 1
	case "HTTP":
		return 2
	default:
		return 3
	}
}

func sortSessionMaps(rows []map[string]any, key, dir string) []map[string]any {
	less := func(i, j int) bool {
		a, b := rows[i], rows[j]
		ka, kb := sessionMediaKeyOf(a), sessionMediaKeyOf(b)
		var cmp int
		switch key {
		case "role":
			cmp = sessionRoleRank(asStr(a["role"])) - sessionRoleRank(asStr(b["role"]))
		case "peer":
			cmp = strings.Compare(asStr(a["peer_ip"])+":"+asStr(a["peer_port"]), asStr(b["peer_ip"])+":"+asStr(b["peer_port"]))
		case "id":
			cmp = strings.Compare(asStr(a["id"]), asStr(b["id"]))
		default:
			switch {
			case ka == "" && kb != "":
				cmp = 1
			case ka != "" && kb == "":
				cmp = -1
			default:
				cmp = strings.Compare(strings.ToLower(ka), strings.ToLower(kb))
				if dir == "desc" {
					cmp = -cmp
				}
			}
		}
		if cmp == 0 && key != "role" {
			cmp = sessionRoleRank(asStr(a["role"])) - sessionRoleRank(asStr(b["role"]))
		}
		if cmp == 0 {
			cmp = strings.Compare(asStr(a["id"]), asStr(b["id"]))
		}
		if dir == "desc" && (key == "role" || key == "peer" || key == "id") {
			return cmp > 0
		}
		return cmp < 0
	}
	sort.SliceStable(rows, less)
	return rows
}

func groupSessionsByMedia(rows []map[string]any) []map[string]any {
	out := make([]map[string]any, 0)
	var cur map[string]any
	var list []map[string]any
	flush := func() {
		if cur == nil {
			return
		}
		cur["Sessions"] = list
		cur["Count"] = len(list)
		out = append(out, cur)
		cur, list = nil, nil
	}
	for _, row := range rows {
		key := sessionMediaKeyOf(row)
		label := key
		if label == "" {
			label = "未关联"
		}
		if cur == nil || asStr(cur["Key"]) != label {
			flush()
			cur = map[string]any{"Key": label, "MediaKey": key}
			list = nil
		}
		list = append(list, row)
	}
	flush()
	return out
}

func sortStreamMaps(rows []map[string]any, key, dir string) []map[string]any {
	less := func(i, j int) bool {
		a, b := rows[i], rows[j]
		var cmp int
		switch key {
		case "codec", "video_codec":
			cmp = strings.Compare(asStr(a["video_codec"]), asStr(b["video_codec"]))
		case "viewers", "clients":
			cmp = cmpFloat(asF(a["totalReaderCount"]), asF(b["totalReaderCount"]))
		case "in_bps":
			cmp = cmpFloat(asF(a["in_bps"]), asF(b["in_bps"]))
		case "out_bps":
			cmp = cmpFloat(asF(a["out_bps"]), asF(b["out_bps"]))
		case "alive", "duration":
			cmp = cmpFloat(asF(a["aliveSecond"]), asF(b["aliveSecond"]))
		case "status":
			cmp = strings.Compare(asStr(a["status"]), asStr(b["status"]))
		case "schema", "origin_schema":
			cmp = strings.Compare(asStr(a["origin_schema"]), asStr(b["origin_schema"]))
		default:
			cmp = strings.Compare(asStr(a["app"])+"/"+asStr(a["stream"]), asStr(b["app"])+"/"+asStr(b["stream"]))
		}
		if cmp == 0 {
			cmp = strings.Compare(asStr(a["stream"]), asStr(b["stream"]))
		}
		if dir == "asc" {
			return cmp < 0
		}
		return cmp > 0
	}
	sort.SliceStable(rows, less)
	return rows
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func listQueryValues(lq listQuery) url.Values {
	q := url.Values{}
	if lq.Q != "" {
		q.Set("q", lq.Q)
	}
	if lq.Sort != "" {
		q.Set("sort", lq.Sort)
	}
	if lq.Dir != "" {
		q.Set("dir", lq.Dir)
	}
	if lq.Page > 1 {
		q.Set("page", strconv.Itoa(lq.Page))
	}
	if lq.Size > 0 && lq.Size != defaultPageSize {
		q.Set("size", strconv.Itoa(lq.Size))
	}
	return q
}

func applyListForm(c *gin.Context) {
	if v, ok := c.GetPostForm("view_q"); ok {
		c.Set("list_q", strings.TrimSpace(v))
	}
	if v, ok := c.GetPostForm("view_sort"); ok {
		c.Set("list_sort", strings.TrimSpace(v))
	}
	if v, ok := c.GetPostForm("view_dir"); ok {
		c.Set("list_dir", strings.TrimSpace(v))
	}
	if v, ok := c.GetPostForm("view_page"); ok {
		c.Set("list_page", strings.TrimSpace(v))
	}
	if v, ok := c.GetPostForm("view_size"); ok {
		c.Set("list_size", strings.TrimSpace(v))
	}
}

func uniqueNonEmpty(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
		if len(out) >= 100 {
			break
		}
	}
	return out
}

func batchResultMessage(ok, fail int, verb string) string {
	if ok == 0 && fail == 0 {
		return "请先勾选要操作的行"
	}
	if fail == 0 {
		return "已" + verb + " " + strconv.Itoa(ok) + " 项"
	}
	return verb + "成功 " + strconv.Itoa(ok) + " / 失败 " + strconv.Itoa(fail)
}

func parseStreamSID(sid string) (node, vhost, app, stream string, ok bool) {
	parts := strings.SplitN(strings.TrimSpace(sid), "|", 4)
	if len(parts) != 4 {
		return "", "", "", "", false
	}
	node, vhost, app, stream = parts[0], parts[1], parts[2], parts[3]
	return node, vhost, app, stream, app != "" && stream != ""
}

func mergeQuery(base url.Values, extra url.Values) url.Values {
	out := cloneValues(base)
	for k, vs := range extra {
		cp := make([]string, len(vs))
		copy(cp, vs)
		out[k] = cp
	}
	return out
}
