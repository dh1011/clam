# clam for Bash. Load it from ~/.bashrc with:
#   eval "$(clam init bash)"
# English lines whose first word is not a command get an answer; other
# unknown commands keep Bash's usual "command not found" behavior.

_CLAM_BIN='@CLAM@'
export CLAM_SESSION_ID="bash-$$-$RANDOM"

# Terminals known to draw color emoji get emoji markers; others keep the
# ASCII ones. Set CLAM_EMOJI=1 or 0 to override the guess.
_clam_emoji() {
    case ${CLAM_EMOJI-} in 1) return 0;; 0) return 1;; esac
    [[ ${LC_ALL:-${LC_CTYPE:-${LANG-}}} == *[Uu][Tt][Ff]-8* ||
       ${LC_ALL:-${LC_CTYPE:-${LANG-}}} == *[Uu][Tt][Ff]8* ]] || return 1
    [[ ${TERM-} == linux ]] && return 1
    [[ -n ${KITTY_WINDOW_ID-}${WEZTERM_PANE-}${GHOSTTY_RESOURCES_DIR-}${WT_SESSION-}${KONSOLE_VERSION-}${VTE_VERSION-} ]] && return 0
    case ${TERM-} in xterm-kitty|xterm-ghostty|wezterm|foot*) return 0;; esac
    case ${TERM_PROGRAM-} in iTerm.app|WezTerm|ghostty|vscode|Apple_Terminal|Hyper|Tabby) return 0;; esac
    return 1
}
# on, auto-approve, off
if _clam_emoji; then
    _CLAM_MARKS=('🐚' '🔥' '💤')
else
    _CLAM_MARKS=('[c+]' '[c!]' '[c-]')
fi

# Refresh the marker on every prompt so it stays correct when another tool
# (such as a Python venv) rewrites PS1.
_clam_prompt_status() {
    [[ $- == *i* ]] || return
    local mark
    for mark in '[c+]' '[c!]' '[c-]' '🐚' '🔥' '💤'; do
        PS1=${PS1//"$mark "/}
    done
    if [[ $_CLAM_ENABLED == on && ${CLAM_AUTO_APPROVE-} == 1 ]]; then
        PS1="${_CLAM_MARKS[1]} $PS1"
    elif [[ $_CLAM_ENABLED == on ]]; then
        PS1="${_CLAM_MARKS[0]} $PS1"
    else
        PS1="${_CLAM_MARKS[2]} $PS1"
    fi
}

_clam_install_prompt_hook() {
    local declaration
    declaration=$(declare -p PROMPT_COMMAND 2>/dev/null) || declaration=
    if [[ $declaration == declare\ -a* ]]; then
        [[ " ${PROMPT_COMMAND[*]} " == *" _clam_prompt_status "* ]] ||
            PROMPT_COMMAND+=(_clam_prompt_status)
    elif [[ ${PROMPT_COMMAND-} != *"_clam_prompt_status"* ]]; then
        PROMPT_COMMAND="${PROMPT_COMMAND:+$PROMPT_COMMAND$'\n'}_clam_prompt_status"
    fi
}

_CLAM_WAS_ENABLED=${_CLAM_ENABLED:-off}
_CLAM_ENABLED=off
_clam_install_prompt_hook

clam() {
    case $1 in
        on)
            # Self-contained and never re-entered: tools that copy only
            # some functions into a new shell (or a missing clam binary)
            # would otherwise send it back into itself, forking without end.
            command_not_found_handle() {
                if [[ -z ${_CLAM_IN_HANDLER-} ]]; then
                    local _CLAM_IN_HANDLER=1 line
                    printf -v line '%s ' "$@"
                    line=${line% }
                    if [[ -x ${_CLAM_BIN-} ]] &&
                        "$_CLAM_BIN" check -- "$line" >/dev/null 2>&1; then
                        "$_CLAM_BIN" ask -- "$line"
                        return
                    fi
                fi
                printf 'bash: %s: command not found\n' "$1" >&2
                return 127
            }
            _CLAM_ENABLED=on
            _clam_prompt_status
            [[ $2 == --quiet ]] ||
                printf 'clam: on — ask a question at the prompt; use "clam off" to disable.\n'
            ;;
        off)
            unset -f command_not_found_handle
            _CLAM_ENABLED=off
            _clam_prompt_status
            printf 'clam: off\n'
            ;;
        agent)
            if [[ -z $2 ]]; then
                "$_CLAM_BIN" agents
            elif "$_CLAM_BIN" agents "$2"; then
                export CLAM_AGENT=$2
                printf 'clam: using %s in this terminal\n' "$2"
            else
                return 2
            fi
            ;;
        auto)
            case $2 in
                on)
                    export CLAM_AUTO_APPROVE=1
                    printf 'clam: auto-approve on — commands and file writes run without asking\n'
                    ;;
                off)
                    unset CLAM_AUTO_APPROVE
                    printf 'clam: auto-approve off\n'
                    ;;
                *)   printf 'usage: clam auto on|off\n' >&2; return 2;;
            esac
            _clam_prompt_status
            ;;
        *)   "$_CLAM_BIN" "$@";;
    esac
}

if [[ ${CLAM_DEFAULT:-on} == off ]]; then
    [[ $_CLAM_WAS_ENABLED == on ]] && unset -f command_not_found_handle
    _clam_prompt_status
else
    clam on --quiet
fi
unset _CLAM_WAS_ENABLED
