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

type PushArgs struct {
	Remote string `json:"remote"`
	Branch string `json:"branch"`
	Commit string `json:"commit"`
}

type PRArgs struct {
	Branch string `json:"branch"`
	Title  string `json:"title"`
}
