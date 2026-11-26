# clam for zsh: when you type an English sentence at the prompt, send it to
# an LLM and stream the answer instead of running it. Anything else is left
# alone and zsh behaves normally. Load it from ~/.zshrc with:
#   eval "$(clam init zsh)"

typeset -g _CLAM_BIN='@CLAM@'
export CLAM_SESSION_ID="zsh-$$-$RANDOM"

typeset -g _CLAM_WAS_ENABLED=${_CLAM_ENABLED:-off}
typeset -g _CLAM_ENABLED=off

# Terminals known to draw color emoji get emoji markers; others keep the
# ASCII ones. Set CLAM_EMOJI=1 or 0 to override the guess.
_clam_emoji() {
  emulate -L zsh
  case ${CLAM_EMOJI-} in 1) return 0;; 0) return 1;; esac
  [[ ${(L)${LC_ALL:-${LC_CTYPE:-${LANG-}}}} == *utf(-|)8* ]] || return 1
  [[ ${TERM-} == linux ]] && return 1
  [[ -n ${KITTY_WINDOW_ID-}${WEZTERM_PANE-}${GHOSTTY_RESOURCES_DIR-}${WT_SESSION-}${KONSOLE_VERSION-}${VTE_VERSION-} ]] && return 0
  case ${TERM-} in xterm-kitty|xterm-ghostty|wezterm|foot*) return 0;; esac
  case ${TERM_PROGRAM-} in iTerm.app|WezTerm|ghostty|vscode|Apple_Terminal|Hyper|Tabby) return 0;; esac
  return 1
}
# on, auto-approve, off
if _clam_emoji; then
  typeset -ga _CLAM_MARKS=('🐚' '🔥' '💤')
else
  typeset -ga _CLAM_MARKS=('[c+]' '[c!]' '[c-]')
fi

_clam_prompt_status() {
  emulate -L zsh
  local mark
  for mark in '[c+]' '[c!]' '[c-]' '🐚' '🔥' '💤'; do
    RPROMPT=${RPROMPT#"$mark "}
  done
  if [[ $_CLAM_ENABLED == on && ${CLAM_AUTO_APPROVE-} == 1 ]]; then
    RPROMPT="$_CLAM_MARKS[2] $RPROMPT"
  elif [[ $_CLAM_ENABLED == on ]]; then
    RPROMPT="$_CLAM_MARKS[1] $RPROMPT"
  else
    RPROMPT="$_CLAM_MARKS[3] $RPROMPT"
  fi
}

# Common English words; lines starting with one are checked even when the
# word happens to be a real command (e.g. /usr/bin/what). Loaded on first
# `clam on` so a terminal that starts with clam off pays nothing.
typeset -gA _clam_words
_clam_load_words() {
  local w
  for w in ${(f)"$("$_CLAM_BIN" words)"}; do
    _clam_words[$w]=1
  done
}

# Decide whether the line is English. On yes, blank the command so nothing
# runs and queue the message; otherwise leave BUFFER untouched.
_clam_check() {
  emulate -L zsh
  setopt extendedglob

  local line=${${BUFFER##[[:space:]]#}%%[[:space:]]#}
  local first=${${(z)line}[1]}
  local -a flags

  if (( $+commands[$first] || $+builtins[$first] || $+aliases[$first] ||
        $+functions[$first] || ${reswords[(Ie)$first]} )); then
    flags=(--exists)
    # Known command that isn't an English word: run it without delay.
    (( $+_clam_words[${(L)first}] )) || return
  fi

  if [[ -n $line ]] && "$_CLAM_BIN" check $flags -- "$line" >/dev/null; then
    print -s -- "$BUFFER"
    # Keep the typed text on screen but accept an empty command, then ask
    # the model just before the next prompt, once the terminal is free.
    POSTDISPLAY=$BUFFER
    BUFFER=
    _clam_query=$line
  fi
}

# Enter always reaches zsh's real accept-line, even if the check errors out.
_clam_accept_line() {
  { _clam_check } always { TRY_BLOCK_ERROR=0 }
  zle .accept-line
}

typeset -g _clam_query
_clam_precmd() {
  [[ -n $_clam_query ]] || return
  local query=$_clam_query
  _clam_query=
  "$_CLAM_BIN" ask -- "$query"
}

# `clam on` / `clam off` toggle it in this terminal only.
clam() {
  case $1 in
    on)
      (( ${#_clam_words} )) || _clam_load_words
      zle -N accept-line _clam_accept_line
      add-zsh-hook precmd _clam_precmd
      _CLAM_ENABLED=on
      _clam_prompt_status
      [[ $2 == --quiet ]] || print "clam: on"
      ;;
    off)
      zle -A .accept-line accept-line
      add-zsh-hook -d precmd _clam_precmd
      _CLAM_ENABLED=off
      _clam_prompt_status
      print "clam: off"
      ;;
    agent)
      if [[ -z $2 ]]; then
        "$_CLAM_BIN" agents
      elif "$_CLAM_BIN" agents "$2"; then
        export CLAM_AGENT=$2
        print "clam: using $2 in this terminal"
      else
        return 2
      fi
      ;;
    auto)
      case $2 in
        on)
          export CLAM_AUTO_APPROVE=1
          print "clam: auto-approve on — commands and file writes run without asking"
          ;;
        off)
          unset CLAM_AUTO_APPROVE
          print "clam: auto-approve off"
          ;;
        *)
          print "usage: clam auto on|off"
          return 1
          ;;
      esac
      _clam_prompt_status
      ;;
    *)
      "$_CLAM_BIN" "$@"
      ;;
  esac
}

autoload -Uz add-zsh-hook
add-zsh-hook precmd _clam_prompt_status

# Set CLAM_DEFAULT=off before loading to start new terminals with it off.
if [[ ${CLAM_DEFAULT:-on} == off ]]; then
  if [[ $_CLAM_WAS_ENABLED == on ]]; then
    zle -A .accept-line accept-line
    add-zsh-hook -d precmd _clam_precmd
  fi
  _clam_prompt_status
  print "clam is off. Run 'clam on' to enable it in this terminal."
else
  clam on --quiet
fi
unset _CLAM_WAS_ENABLED
