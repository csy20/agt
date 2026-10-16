package cli

import (
	"fmt"
	"os"
	"strings"
)

func completion(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: agt completion bash|zsh|fish|powershell")
		return 2
	}
	script, ok := completionScript(args[0])
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown shell %q\nusage: agt completion bash|zsh|fish|powershell\n", args[0])
		return 2
	}
	fmt.Print(script)
	return 0
}

func rootCommands() []string {
	return []string{"fix", "feature", "refactor", "profile", "approve", "run", "doctor", "setup", "tui", "session", "model", "agent", "completion", "version", "help"}
}

func runCommands() []string {
	return []string{"create", "status", "attach", "list", "diff", "export", "apply", "pause", "resume", "budget-resume", "cancel", "cleanup"}
}

func completionScript(shell string) (string, bool) {
	roots := strings.Join(rootCommands(), " ")
	runs := strings.Join(runCommands(), " ")
	sessions := strings.Join(sessionCommands(), " ")
	models := strings.Join(modelCommands(), " ")
	agents := strings.Join(agentCommands(), " ")
	switch shell {
	case "bash":
		return fmt.Sprintf(`_agt() {
  local cur prev
  COMPREPLY=()
  cur="${COMP_WORDS[COMP_CWORD]}"
  prev="${COMP_WORDS[COMP_CWORD-1]}"
  if [[ ${COMP_CWORD} -eq 1 ]]; then
    COMPREPLY=( $(compgen -W "%s" -- "$cur") )
    return
  fi
  if [[ ${prev} == run ]]; then
    COMPREPLY=( $(compgen -W "%s" -- "$cur") )
  fi
  if [[ ${prev} == session ]]; then
    COMPREPLY=( $(compgen -W "%s" -- "$cur") )
  fi
  if [[ ${prev} == model ]]; then
    COMPREPLY=( $(compgen -W "%s" -- "$cur") )
  fi
  if [[ ${prev} == agent ]]; then
    COMPREPLY=( $(compgen -W "%s" -- "$cur") )
  fi
}
complete -F _agt agt
`, roots, runs, sessions, models, agents), true
	case "zsh":
		return fmt.Sprintf(`#compdef agt
_agt() {
  local -a commands runs sessions models agents
  commands=(%s)
  runs=(%s)
  sessions=(%s)
  models=(%s)
  agents=(%s)
  if (( CURRENT == 2 )); then
    _describe command commands
    return
  fi
  if [[ ${words[2]} == run ]]; then
    _describe run runs
  fi
  if [[ ${words[2]} == session ]]; then
    _describe session sessions
  fi
  if [[ ${words[2]} == model ]]; then
    _describe model models
  fi
  if [[ ${words[2]} == agent ]]; then
    _describe agent agents
  fi
}
compdef _agt agt
`, roots, runs, sessions, models, agents), true
	case "fish":
		return fmt.Sprintf(`complete -c agt -n "not __fish_seen_subcommand_from %s" -a "%s"
complete -c agt -n "__fish_seen_subcommand_from run" -a "%s"
complete -c agt -n "__fish_seen_subcommand_from session" -a "%s"
complete -c agt -n "__fish_seen_subcommand_from model" -a "%s"
complete -c agt -n "__fish_seen_subcommand_from agent" -a "%s"
`, roots, roots, runs, sessions, models, agents), true
	case "powershell":
		var rootsQ, runsQ, sessionsQ, modelsQ, agentsQ []string
		for _, name := range rootCommands() {
			rootsQ = append(rootsQ, "'"+name+"'")
		}
		for _, name := range runCommands() {
			runsQ = append(runsQ, "'"+name+"'")
		}
		for _, name := range sessionCommands() {
			sessionsQ = append(sessionsQ, "'"+name+"'")
		}
		for _, name := range modelCommands() {
			modelsQ = append(modelsQ, "'"+name+"'")
		}
		for _, name := range agentCommands() {
			agentsQ = append(agentsQ, "'"+name+"'")
		}
		return fmt.Sprintf(`Register-ArgumentCompleter -Native -CommandName agt -ScriptBlock {
  param($wordToComplete, $commandAst, $cursorPosition)
  $roots = @(%s)
  $runs = @(%s)
  $sessions = @(%s)
  $models = @(%s)
  $agents = @(%s)
  $words = @($commandAst.CommandElements | ForEach-Object { $_.ToString() })
  $choices = $roots
  if ($words.Count -ge 2 -and $words[1] -eq 'run') { $choices = $runs }
  if ($words.Count -ge 2 -and $words[1] -eq 'session') { $choices = $sessions }
  if ($words.Count -ge 2 -and $words[1] -eq 'model') { $choices = $models }
  if ($words.Count -ge 2 -and $words[1] -eq 'agent') { $choices = $agents }
  $choices | Where-Object { $_ -like "$wordToComplete*" } | ForEach-Object {
    [System.Management.Automation.CompletionResult]::new($_, $_, 'ParameterValue', $_)
  }
}
`, strings.Join(rootsQ, ","), strings.Join(runsQ, ","), strings.Join(sessionsQ, ","), strings.Join(modelsQ, ","), strings.Join(agentsQ, ",")), true
	default:
		return "", false
	}
}
