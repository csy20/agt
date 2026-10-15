package tools

// Definitions is the OpenAI-compatible tool list. The model may request these
// names only; the policy package decides whether a request can run.
func Definitions() []any {
	object := func(props map[string]any, required []string) map[string]any {
		return map[string]any{
			"type":                 "object",
			"properties":           props,
			"required":             required,
			"additionalProperties": false,
		}
	}
	fn := func(name, desc string, params map[string]any) map[string]any {
		return map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        name,
				"description": desc,
				"parameters":  params,
			},
		}
	}
	str := map[string]any{"type": "string"}
	num := map[string]any{"type": "integer"}
	return []any{
		fn("search_code", "Search the repository for a literal string.", object(map[string]any{"query": str}, []string{"query"})),
		fn("read_file_range", "Read an inclusive 1-based line range from one file.", object(map[string]any{"path": str, "start": num, "end": num}, []string{"path", "start", "end"})),
		fn("git_diff", "Show the uncommitted diff in the run worktree.", object(map[string]any{}, nil)),
		fn("run_test", "Run the user-selected test command inside the sandbox. The command is fixed for the run.", object(map[string]any{}, nil)),
		fn("apply_patch", "Replace one exact occurrence of old text with new text in a worktree file.", object(map[string]any{"path": str, "old": str, "new": str}, []string{"path", "old", "new"})),
		fn("write_file", "Create or replace a text file. before_sha256 must match the current file, or be empty when the file must not exist.", object(map[string]any{"path": str, "content": str, "before_sha256": str}, []string{"path", "content", "before_sha256"})),
		fn("delete_file", "Delete one file whose bytes hash to before_sha256. Directories are not deleted.", object(map[string]any{"path": str, "before_sha256": str}, []string{"path", "before_sha256"})),
	}
}
