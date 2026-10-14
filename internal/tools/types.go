package tools

const Version = "tools.v1"

type EmptyArgs struct{}

type SearchArgs struct {
	Query string `json:"query"`
}

type ReadArgs struct {
	Path  string `json:"path"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}

type PatchArgs struct {
	Path string `json:"path"`
	Old  string `json:"old"`
	New  string `json:"new"`
}

type WriteArgs struct {
	Path         string `json:"path"`
	Content      string `json:"content"`
	BeforeSHA256 string `json:"before_sha256"`
}

type DeleteArgs struct {
	Path         string `json:"path"`
	BeforeSHA256 string `json:"before_sha256"`
}

// FileResult records a create, edit, or delete. Hashes are lowercase SHA-256.
type FileResult struct {
	Path         string `json:"path"`
	BeforeSHA256 string `json:"before_sha256"`
	AfterSHA256  string `json:"after_sha256"`
	Action       string `json:"action"`
}

type PushArgs struct {
	Remote string `json:"remote"`
	Branch string `json:"branch"`
	Commit string `json:"commit"`
}

type PRArgs struct {
	Branch string `json:"branch"`
	Title  string `json:"title"`
}
